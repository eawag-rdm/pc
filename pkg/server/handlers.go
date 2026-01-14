package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
	"github.com/eawag-rdm/pc/pkg/utils"
)

// Handler processes HTTP requests for the PC server
type Handler struct {
	pcConfig    *config.Config
	serverCfg   Config
}

// NewHandler creates a new handler with the given configuration
func NewHandler(pcConfig *config.Config, serverCfg Config) *Handler {
	return &Handler{
		pcConfig:  pcConfig,
		serverCfg: serverCfg,
	}
}

// AnalyzeRequest represents the request body for the analyze endpoint
type AnalyzeRequest struct {
	PackageID string `json:"package_id"`
	CkanURL   string `json:"ckan_url,omitempty"` // Optional override for CKAN URL
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
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
		respondError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body: "+err.Error())
		return
	}

	// 2. Validate request
	if req.PackageID == "" {
		respondError(w, http.StatusBadRequest, "missing_package_id", "package_id is required")
		return
	}

	// 3. Get CKAN token from context (set by middleware)
	token := GetTokenFromContext(r)
	if token == "" {
		respondError(w, http.StatusUnauthorized, "no_token", "CKAN API token is required")
		return
	}

	// 4. Determine CKAN URL (request override > server config > pc config)
	ckanURL := req.CkanURL
	if ckanURL == "" {
		ckanURL = h.serverCfg.GetCKANBaseURL(h.pcConfig)
	}
	if ckanURL == "" {
		respondError(w, http.StatusInternalServerError, "no_ckan_url", "CKAN URL is not configured")
		return
	}

	// 5. Verify CKAN access with the user's token
	verifyTLS := h.serverCfg.GetVerifyTLS(h.pcConfig)
	if err := VerifyCKANAccess(ckanURL, req.PackageID, token, verifyTLS); err != nil {
		if statusCode, isAuthErr := IsCKANAuthError(err); isAuthErr {
			switch statusCode {
			case http.StatusUnauthorized:
				respondError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			case http.StatusForbidden:
				respondError(w, http.StatusForbidden, "forbidden", err.Error())
			case http.StatusNotFound:
				respondError(w, http.StatusNotFound, "not_found", err.Error())
			default:
				respondError(w, http.StatusBadGateway, "ckan_error", err.Error())
			}
			return
		}
		respondError(w, http.StatusInternalServerError, "ckan_error", "Failed to verify CKAN access: "+err.Error())
		return
	}

	// 6. Create a copy of PC config with the user's token for collection
	pcConfigCopy := *h.pcConfig
	if ckanCollector, ok := pcConfigCopy.Collectors["CkanCollector"]; ok {
		// Create a copy of attrs map
		newAttrs := make(map[string]interface{})
		for k, v := range ckanCollector.Attrs {
			newAttrs[k] = v
		}
		// Override token and URL
		newAttrs["token"] = token
		if req.CkanURL != "" {
			newAttrs["url"] = req.CkanURL
		}
		ckanCollector.Attrs = newAttrs
		pcConfigCopy.Collectors["CkanCollector"] = ckanCollector
	}

	// 7. Collect files from CKAN
	files, err := collectors.CkanCollector(req.PackageID, pcConfigCopy)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "collector_error", "Failed to collect files: "+err.Error())
		return
	}

	if len(files) == 0 {
		respondError(w, http.StatusNotFound, "no_files", "No files found in package '"+req.PackageID+"'")
		return
	}

	// 8. Run checks
	messages := utils.ApplyAllChecks(pcConfigCopy, files, true)

	// 9. Format results as JSON
	formatter := jsonformatter.NewJSONFormatter()
	jsonResult, err := formatter.FormatResults(req.PackageID, "CkanCollector", messages, len(files), helpers.PDFTracker.Files)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "format_error", "Failed to format results: "+err.Error())
		return
	}

	// 10. Return JSON response directly (already formatted)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(jsonResult))
}

// Helper functions for JSON responses
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, status int, code, message string) {
	respondJSON(w, status, ErrorResponse{
		Error: message,
		Code:  code,
	})
}
