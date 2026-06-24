package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/output"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
	"github.com/eawag-rdm/pc/pkg/utils"
)

// Handler processes HTTP requests for the PC server
type Handler struct {
	pcConfig  *config.Config
	serverCfg Config

	// logger is the slog JSON handler used for request/access and lifecycle
	// logging. It is never used for check Messages (those stay in GlobalLogger).
	logger *slog.Logger
	// contactMsg is the contact suffix shown in error envelopes, sourced once
	// from the PC config's [server] section.
	contactMsg string
	// logClientIP gates whether the client IP is recorded in access logs.
	logClientIP bool

	// analysisMu serializes the part of Analyze that touches process-global
	// state (output.GlobalLogger and helpers.PDFTracker): the per-request reset,
	// the analysis that accumulates into those globals, and the snapshot read
	// used to build the response. Without this, one request's reset can wipe (or
	// race with) another in-flight request's accumulated Messages/PDF notes,
	// bleeding data across responses (§6, §9). It is effectively a
	// concurrency=1 gate; the configurable maxConcurrentAnalyses semaphore and
	// per-request logger/tracker instances arrive in a later step.
	analysisMu sync.Mutex
}

// NewHandler creates a new handler with the given configuration. The slog
// logger writes JSON access records to stdout.
func NewHandler(pcConfig *config.Config, serverCfg Config, logger *slog.Logger) *Handler {
	contact := DefaultContactMessage
	logClientIP := true
	if pcConfig != nil && pcConfig.Server != nil {
		if pcConfig.Server.ContactMessage != "" {
			contact = pcConfig.Server.ContactMessage
		}
		logClientIP = pcConfig.Server.LogClientIP
	}

	return &Handler{
		pcConfig:    pcConfig,
		serverCfg:   serverCfg,
		logger:      logger,
		contactMsg:  contact,
		logClientIP: logClientIP,
	}
}

// DefaultContactMessage is used when no [server] contactMessage is configured.
const DefaultContactMessage = "If you can't resolve this yourself, please contact rdm@eawag.ch."

// AnalyzeRequest represents the request body for the analyze endpoint. The CKAN
// base URL is intentionally server-side only and is not accepted from the client
// (it was an SSRF + token-exfiltration vector; see §2).
type AnalyzeRequest struct {
	PackageID string `json:"package_id"`
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
}

// Health handles GET /health
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		Version:   "1.0.0",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// Analyze handles POST /api/v1/analyze
func (h *Handler) Analyze(w http.ResponseWriter, r *http.Request) {
	// 1. Parse request body
	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, CodeInvalidRequest)
		return
	}

	// Record package_id for the access log. This writes through the holder the
	// access-log middleware installed, so the emitted record carries it.
	setPackageID(r, req.PackageID)

	// 2. Validate request
	if req.PackageID == "" {
		writeError(w, r, CodeMissingPackage)
		return
	}

	// 3. Get CKAN token from context (set by middleware). The token is OPTIONAL:
	// an empty token follows the public-package path.
	token := GetTokenFromContext(r)

	// 4. Determine CKAN URL (server-side only, single source = collector config).
	ckanURL := h.serverCfg.GetCKANBaseURL(h.pcConfig)
	if ckanURL == "" {
		writeError(w, r, CodeInternalError)
		return
	}

	// 5. Verify CKAN access with the user's token. This is a read-only CKAN call
	// that does not touch process-global state, so it runs outside analysisMu.
	verifyTLS := h.serverCfg.GetVerifyTLS(h.pcConfig)
	if err := VerifyCKANAccess(ckanURL, req.PackageID, token, verifyTLS); err != nil {
		if statusCode, isAuthErr := IsCKANAuthError(err); isAuthErr {
			switch statusCode {
			case http.StatusUnauthorized:
				writeError(w, r, CodeInvalidToken)
			case http.StatusForbidden:
				writeError(w, r, CodeAccessDenied)
			case http.StatusNotFound:
				// Nonexistent and private-unauthorized both surface as 404 under
				// CKAN's default reveal_private_datasets=false (§3).
				writeError(w, r, CodePackageNotFound)
			default:
				writeError(w, r, CodeCKANUnavailable)
			}
			return
		}
		writeError(w, r, CodeCKANUnavailable)
		return
	}

	// 6-9. Run the analysis under analysisMu: the per-request reset, the
	// collect/check work that accumulates into the process-global GlobalLogger
	// and PDFTracker, and the snapshot read used to build the body must be one
	// serialized unit, or a concurrent request's reset bleeds into / races with
	// this one (§6, §9).
	jsonResult, errCode := h.runAnalysis(req.PackageID, token)
	if errCode != "" {
		writeError(w, r, errCode)
		return
	}

	// 10. Add request_id to the response body (additive) and return.
	body, err := withRequestID(jsonResult, GetRequestID(r))
	if err != nil {
		writeError(w, r, CodeInternalError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// runAnalysis performs the global-state-touching part of an analysis under
// analysisMu and returns the formatted JSON body. On failure it returns an empty
// body and a non-empty error catalogue code for the caller to render. Holding
// analysisMu across the reset, the collect/check work, and the PDFTracker
// snapshot read keeps the process-global GlobalLogger/PDFTracker from leaking or
// racing across concurrent requests (§6, §9).
func (h *Handler) runAnalysis(packageID, token string) (string, string) {
	h.analysisMu.Lock()
	defer h.analysisMu.Unlock()

	// Per-request reset of process-global state. GlobalLogger buffers check
	// Messages and PDFTracker accumulates PDF notes; both leak/race across
	// requests if not cleared at the start of each analysis (§6, §9).
	output.GlobalLogger.ClearMessages()
	helpers.PDFTracker.Reset()

	// Deep-copy the PC config (and the CkanCollector Attrs map) per request so
	// one request's token can never bleed into another's analysis (§9).
	pcConfigCopy := deepCopyConfigForRequest(h.pcConfig, token)

	// Collect files from CKAN.
	files, err := collectors.CkanCollector(packageID, pcConfigCopy)
	if err != nil {
		return "", CodeCKANUnavailable
	}
	if len(files) == 0 {
		return "", CodePackageNotFound
	}

	// Run checks (accumulates into GlobalLogger / PDFTracker).
	messages := utils.ApplyAllChecks(pcConfigCopy, files, true)

	// Format results as JSON. PDFTracker.SnapshotFiles takes a locked copy; we
	// also still hold analysisMu, so no concurrent reset/append can intervene.
	formatter := jsonformatter.NewJSONFormatter()
	jsonResult, err := formatter.FormatResults(packageID, "CkanCollector", messages, len(files), helpers.PDFTracker.SnapshotFiles())
	if err != nil {
		return "", CodeInternalError
	}
	return jsonResult, ""
}

// deepCopyConfigForRequest returns a copy of pcConfig safe for concurrent use
// by a single request: the Collectors map and the CkanCollector Attrs map are
// duplicated so the per-request token override never mutates shared state.
func deepCopyConfigForRequest(pcConfig *config.Config, token string) config.Config {
	cfgCopy := *pcConfig

	// Deep-copy the Collectors map so we don't share it with other requests.
	newCollectors := make(map[string]*config.CollectorConfig, len(pcConfig.Collectors))
	for name, cc := range pcConfig.Collectors {
		newAttrs := make(map[string]interface{}, len(cc.Attrs))
		for k, v := range cc.Attrs {
			newAttrs[k] = v
		}
		newCollectors[name] = &config.CollectorConfig{Attrs: newAttrs}
	}
	cfgCopy.Collectors = newCollectors

	// Override the CkanCollector token for this request only.
	if ckanCollector, ok := cfgCopy.Collectors["CkanCollector"]; ok {
		ckanCollector.Attrs["token"] = token
	}

	return cfgCopy
}

// withRequestID injects request_id into an already-formatted JSON object,
// preserving the existing shape (additive only).
func withRequestID(jsonResult, requestID string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonResult), &obj); err != nil {
		return nil, err
	}
	idBytes, err := json.Marshal(requestID)
	if err != nil {
		return nil, err
	}
	obj["request_id"] = idBytes
	return json.Marshal(obj)
}

// respondJSON writes a JSON body with the given status.
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
