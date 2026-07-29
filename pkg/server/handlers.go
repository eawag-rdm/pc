package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/output"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

// maxAnalyzeBodyBytes caps the request body. The body is a tiny JSON object
// ({"package_id":"..."}); 4 KiB is generous and bounds memory/abuse (spec §2).
const maxAnalyzeBodyBytes = 4 << 10 // 4 KiB

// serverVersion is reported by /health and /ready.
const serverVersion = "1.0.0"

// The fallback timeouts (applied only when the config or its Server section is
// nil, i.e. hand-built configs in tests — ParseConfig always sets both fields)
// derive from the same config defaults, so there is one source of truth.
const (
	defaultRequestTimeout     = time.Duration(config.DefaultServerRequestTimeoutSeconds) * time.Second
	defaultCkanRequestTimeout = time.Duration(config.DefaultServerCkanRequestTimeoutSeconds) * time.Second
)

// packageIDPattern is CKAN's name grammar; it also matches lowercase UUIDs
// (spec §2). A package_id that does not match is rejected as
// invalid_package_name BEFORE any CKAN call.
var packageIDPattern = regexp.MustCompile(`^[a-z0-9_-]{2,100}$`)

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

	// analysisMu is the single serialization gate (concurrency = 1, §4/§9). It
	// serializes the part of Analyze that touches process-global state
	// (output.GlobalLogger and helpers.PDFTracker): the per-request reset, the
	// analysis that accumulates into those globals, and the snapshot read used to
	// build the response. Without it, one request's reset can wipe (or race with)
	// another in-flight request's accumulated Messages/PDF notes, bleeding data
	// across responses. The Concurrency middleware's gate channel (sem) is the
	// front door to this same single slot: it lets a request WAIT up to
	// analysisBusyWait for the slot and rejects with service_busy on timeout, so
	// the middleware gate and analysisMu are one coherent concurrency=1 gate
	// rather than two overlapping ones.
	analysisMu sync.Mutex

	// limiter is the proxy-aware fixed-window rate limiter applied to /analyze
	// only (§4). It is nil only in handler-isolation tests that never exercise
	// the rate-limit middleware.
	limiter *rateLimiter
	// sem is the analysis gate (§4): a buffered channel of capacity 1, mirroring
	// analysisMu's single slot. The Concurrency middleware acquires it (waiting up
	// to analysisBusyWait) before the handler runs and releases it when the
	// handler returns. A nil channel disables the gate (used by
	// handler-isolation tests).
	sem chan struct{}
	// analysisBusyWait is how long the Concurrency middleware waits for the gate
	// slot before rejecting with service_busy (503). Sourced from
	// [server] analysisBusyWaitSeconds (default 2s).
	analysisBusyWait time.Duration

	// readiness computes and caches the /ready verdict (CKAN reachable AND
	// storage mount readable), refreshed at most every readinessTTL (§1).
	readiness *readinessChecker

	// allowedOrigins is the CORS allow-list of origin URLs (§9). Empty means CORS
	// is effectively disabled (no Access-Control-Allow-Origin is emitted).
	allowedOrigins []string

	// draining is set (atomically) when graceful shutdown begins. While set, new
	// requests are rejected with server_restarting (503) so in-flight analyses
	// can drain (§9).
	draining atomic.Bool

	// cache is the per-package on-disk result cache (nil = caching disabled).
	// It is read and written only inside runAnalysis, under analysisMu.
	cache *resultCache

	// alerter emails the admin list when the server returns a server-fault
	// response (internal_error, a recovered panic, or resource_unreadable). It is
	// nil when [server.smtp] is disabled (no host or no recipients); Notify/Close
	// are nil-safe, so a nil alerter is simply a no-op.
	alerter *alerter
}

// NewHandler creates a new handler with the given configuration. The slog
// logger writes JSON access records to stdout.
func NewHandler(pcConfig *config.Config, serverCfg Config, logger *slog.Logger) *Handler {
	contact := DefaultContactMessage
	logClientIP := true
	var limiter *rateLimiter
	var sem chan struct{}
	var allowedOrigins []string
	var alerter *alerter
	// The analysis gate is ALWAYS concurrency = 1 for race-safety: only one
	// analysis may touch the process-global GlobalLogger/PDFTracker at a time
	// (§4/§9). The busy-wait duration is configurable; the capacity is not.
	busyWait := time.Duration(config.DefaultServerAnalysisBusyWaitSeconds) * time.Second
	if pcConfig != nil && pcConfig.Server != nil {
		s := pcConfig.Server
		if s.ContactMessage != "" {
			contact = s.ContactMessage
		}
		logClientIP = s.LogClientIP
		allowedOrigins = s.AllowedOrigins

		limiter = newRateLimiter(
			s.PerIPRequestsPerHour,
			s.GlobalRequestsPerHour,
			s.BurstFactor,
			s.MaxTrackedRateKeys,
			s.TrustProxyHeaders,
			s.TrustedProxies,
		)
		if s.AnalysisBusyWaitSeconds > 0 {
			busyWait = time.Duration(s.AnalysisBusyWaitSeconds) * time.Second
		}
		sem = make(chan struct{}, 1)

		// Admin alerts: nil unless [server.smtp] is configured (host + recipients).
		// newAlerter returns nil when disabled, so the alerter stays nil on the
		// handler-isolation path (pcConfig/Server nil) as well.
		alerter = newAlerter(s.SMTP, logger)
	}

	h := &Handler{
		pcConfig:         pcConfig,
		serverCfg:        serverCfg,
		logger:           logger,
		contactMsg:       contact,
		logClientIP:      logClientIP,
		limiter:          limiter,
		sem:              sem,
		analysisBusyWait: busyWait,
		allowedOrigins:   allowedOrigins,
		alerter:          alerter,
	}
	h.readiness = newReadinessChecker(h)
	return h
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

// Health handles GET /health. It is a liveness probe: a cheap, static 200 with
// no upstream I/O (§1). It is exempt from the rate limiter and concurrency
// semaphore so load balancers / Docker healthchecks can poll it freely.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, HealthResponse{
		Status:    "ok",
		Version:   serverVersion,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// Ready handles GET /ready. It is a readiness probe: it verifies the CKAN API
// is reachable (cached ~5s) AND the storage mount is readable (§1). Healthy ->
// 200; not ready -> 503 with the service_not_ready envelope. Like /health it is
// exempt from the rate limiter and concurrency semaphore.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.readiness == nil || !h.readiness.isReady() {
		writeError(w, r, CodeServiceNotReady)
		return
	}
	respondJSON(w, http.StatusOK, HealthResponse{
		Status:    "ready",
		Version:   serverVersion,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// Analyze handles POST /api/v1/analyze
func (h *Handler) Analyze(w http.ResponseWriter, r *http.Request) {
	// 1. Cap the request body (spec §2): the body is a tiny JSON object, so an
	// oversized body is rejected as invalid_request rather than read into memory.
	r.Body = http.MaxBytesReader(w, r.Body, maxAnalyzeBodyBytes)

	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, CodeInvalidRequest)
		return
	}

	// Record package_id for the access log. This writes through the holder the
	// access-log middleware installed, so the emitted record carries it.
	setPackageID(r, req.PackageID)

	// 2. Validate request (spec §2). Missing/empty -> missing_package; otherwise
	// it must match CKAN's name grammar (which also matches lowercase UUIDs) or
	// it is rejected as invalid_package_name BEFORE any CKAN call.
	if req.PackageID == "" {
		writeError(w, r, CodeMissingPackage)
		return
	}
	if !packageIDPattern.MatchString(req.PackageID) {
		writeError(w, r, CodeInvalidPackageName)
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

	// 5. Apply the hard request timeout (spec §2) via a context deadline so a
	// runaway analysis returns a clean envelope BEFORE the server WriteTimeout
	// fires. Client disconnects propagate through the same context.
	ctx, cancel := context.WithTimeout(r.Context(), configuredRequestTimeout(h.pcConfig))
	defer cancel()

	// 6-9. Run the single CKAN package_show + analysis under analysisMu: the
	// per-request reset, the collect/check work that accumulates into the
	// process-global GlobalLogger and PDFTracker, and the snapshot read used to
	// build the body must be one serialized unit, or a concurrent request's
	// reset bleeds into / races with this one (§6, §9). The double fetch is
	// collapsed (spec §5): the collector's package_show is the only CKAN call.
	requestID := GetRequestID(r)
	start := time.Now()
	h.logger.LogAttrs(ctx, slog.LevelInfo, "analysis_start",
		slog.String("request_id", requestID),
		slog.String("package_id", req.PackageID),
	)

	jsonResult, fileCount, skippedCount, cached, errCode, errMsg := h.runAnalysis(ctx, req.PackageID, token)

	// A cancelled/expired context means the client went away or the hard
	// timeout fired. Distinguish a deadline (the whole-analysis timeout) from a
	// cancellation and emit the analysis_cancelled lifecycle event (spec §2, §8).
	if ctxErr := ctx.Err(); ctxErr != nil {
		h.logger.LogAttrs(context.Background(), slog.LevelWarn, "analysis_cancelled",
			slog.String("request_id", requestID),
			slog.String("package_id", req.PackageID),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("cause", ctxErr.Error()),
		)
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			// Past the hard upper bound. The deadline bounds the WHOLE analysis
			// (CKAN call + local file reads + checks), so this is an
			// analysis_timeout (504) — NOT ckan_unavailable, which is reserved
			// for CKAN genuinely being unreachable.
			writeError(w, r, CodeAnalysisTimeout)
		} else {
			// Client cancelled / server draining: busy.
			writeError(w, r, CodeServiceBusy)
		}
		return
	}

	// A 403 on an anonymous request means the dataset needs a token, not that
	// the token lacks rights: steer the user to provide one (401 token_required).
	if errCode == CodeAccessDenied && token == "" {
		errCode = CodeTokenRequired
	}

	if errCode != "" {
		if errMsg != "" {
			// A dynamic, user-facing message (malformed_resource): surface it.
			writeErrorMessage(w, r, errCode, errMsg)
		} else {
			writeError(w, r, errCode)
		}
		return
	}

	if cached {
		// Served from the result cache: the expensive checks phase never ran.
		// analysis_cached replaces analysis_done so dashboards can tell the
		// two apart; file/skip counts are not recomputed on this path.
		h.logger.LogAttrs(ctx, slog.LevelInfo, "analysis_cached",
			slog.String("request_id", requestID),
			slog.String("package_id", req.PackageID),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	} else {
		h.logger.LogAttrs(ctx, slog.LevelInfo, "analysis_done",
			slog.String("request_id", requestID),
			slog.String("package_id", req.PackageID),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.Int("file_count", fileCount),
			slog.Int("skipped_count", skippedCount),
		)
	}

	// 10. Add request_id to the response body (additive) and return.
	body, err := withRequestID(jsonResult, requestID)
	if err != nil {
		writeError(w, r, CodeInternalError)
		return
	}

	if h.cache != nil {
		// Cache observability for clients/ops; only meaningful when caching is on.
		if cached {
			w.Header().Set("X-PC-Cache", "hit")
		} else {
			w.Header().Set("X-PC-Cache", "miss")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// configuredRequestTimeout derives the hard request upper bound (spec §2) from
// the [server] requestTimeoutSeconds setting, falling back to
// defaultRequestTimeout when the config (or its Server section) is nil/unset. It
// is the single source of the request-timeout duration so the handler's context
// deadline and the server's socket WriteTimeout stay in sync.
func configuredRequestTimeout(pcConfig *config.Config) time.Duration {
	if pcConfig != nil && pcConfig.Server != nil && pcConfig.Server.RequestTimeoutSeconds > 0 {
		return time.Duration(pcConfig.Server.RequestTimeoutSeconds) * time.Second
	}
	return defaultRequestTimeout
}

// configuredCkanRequestTimeout derives the upper bound for the single CKAN
// package_show call from [server] ckanRequestTimeoutSeconds. It is always at
// most the whole-analysis timeout (enforced by validateServerSettings).
func configuredCkanRequestTimeout(pcConfig *config.Config) time.Duration {
	if pcConfig != nil && pcConfig.Server != nil && pcConfig.Server.CkanRequestTimeoutSeconds > 0 {
		return time.Duration(pcConfig.Server.CkanRequestTimeoutSeconds) * time.Second
	}
	return defaultCkanRequestTimeout
}

// runAnalysis performs the global-state-touching part of an analysis under
// analysisMu and returns the formatted JSON body plus the analyzed-file and
// skipped counts. On failure it returns an empty body and a non-empty error
// catalogue code for the caller to render. Holding analysisMu across the reset,
// the collect/check work, and the PDFTracker snapshot read keeps the
// process-global GlobalLogger/PDFTracker from leaking or racing across
// concurrent requests (§6, §9). The collector's package_show is the single CKAN
// call (spec §5); its outcome drives the error mapping here.
func (h *Handler) runAnalysis(ctx context.Context, packageID, token string) (body string, fileCount, skippedCount int, cached bool, errCode, errMsg string) {
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

	// Single CKAN call: package_show via the collector. Its outcome (a
	// MalformedResourceError, a structured collectors.CKANError, the
	// ErrResourceUnreadable sentinel, or a transport error) maps to the
	// catalogue (spec §5). A genuinely absent / private-unauthorized package is a
	// CKAN 404 surfaced here as package_not_found. The call gets its own, much
	// shorter deadline (ckanRequestTimeoutSeconds, default 10s) nested inside the
	// whole-analysis deadline: a CKAN instance that cannot answer a metadata GET
	// within that window is unavailable (mapped to ckan_unavailable), and must
	// not eat the checks phase's time budget.
	ckanStart := time.Now()
	ckanCtx, ckanCancel := context.WithTimeout(ctx, configuredCkanRequestTimeout(h.pcConfig))
	result, err := collectors.CkanPackageShow(ckanCtx, packageID, pcConfigCopy)
	ckanCancel()

	// Result cache: metadata_modified from the fetched document is the
	// freshness signal (CKAN bumps it on every dataset/resource change). On a
	// hit the expensive file mapping + checks phase is skipped entirely — the
	// request cost one CKAN round-trip. Authorization is unaffected: a caller
	// whose token cannot read the package failed the fetch above and never
	// reaches the cache.
	var metadataModified string
	if err == nil {
		metadataModified, _ = result["metadata_modified"].(string)
		if cachedBody, ok := h.cache.get(packageID, metadataModified); ok {
			h.logCKANOutcome(ctx, packageID, nil, time.Since(ckanStart))
			return cachedBody, 0, 0, true, "", ""
		}
	}

	var files []structs.File
	if err == nil {
		// File mapping + local path resolution works from the fetched document
		// (no further CKAN calls); its Malformed/Unreadable failures share the
		// same catalogue mapping as fetch failures.
		files, err = collectors.CkanFilesFromResult(result, pcConfigCopy)
	}
	h.logCKANOutcome(ctx, packageID, err, time.Since(ckanStart))
	if err != nil {
		code, msg := mapCKANError(err)
		return "", 0, 0, false, code, msg
	}

	// A package that exists (package_show returned 200) but has zero analyzable
	// upload resources — e.g. one whose resources are all external links — is NOT
	// an error: run the checks (which yield no file issues) and return a normal
	// result. package_not_found is reserved for the CKAN 404 above.

	// Run checks: metadata first (pure mapping over the already-fetched result,
	// cannot fail), then the file checks (accumulate into GlobalLogger /
	// PDFTracker). ctx carries the whole-analysis deadline: the checks loop stops
	// between files once it fires, so the spec's hard upper bound holds over the
	// checks phase too — the handler then maps the expired context to
	// analysis_timeout (504).
	md := metadata.CkanMetadataFromJSON(result)
	messages := append(metadata.RunChecks(md), utils.ApplyAllChecks(ctx, pcConfigCopy, files, true)...)

	// Server-mode response discipline (spec §3/§8: no internal paths, no raw
	// diagnostics). The buffered GlobalLogger diagnostics are split by audience:
	// the full raw detail (paths, causes) goes to the server log keyed by
	// request_id, and the response instead gets ONE soft, path-free skip
	// acknowledgement per affected file. The buffer is cleared, so the
	// formatter's warnings[]/errors[] arrays are always empty in server
	// responses. Then any remaining absolute FileStore paths are blanked from
	// the outgoing messages. The CLI shares none of this — its formatters read
	// the untouched GlobalLogger and full paths.
	messages = append(messages, h.convertScanDiagnostics(ctx, packageID)...)
	scrubMessagePaths(messages)

	// Format results as JSON. PDFTracker.SnapshotFiles takes a locked copy; we
	// also still hold analysisMu, so no concurrent reset/append can intervene.
	formatter := jsonformatter.NewJSONFormatter()
	jsonResult, err := formatter.FormatResults(packageID, "CkanCollector", messages, len(files), helpers.PDFTracker.SnapshotFiles())
	if err != nil {
		return "", 0, 0, false, CodeInternalError, ""
	}

	// Store the successful result for future identical-freshness requests.
	// Best-effort: a cache write failure is logged but never fails the request.
	if cacheErr := h.cache.put(packageID, metadataModified, jsonResult); cacheErr != nil {
		h.logger.LogAttrs(ctx, slog.LevelWarn, "result_cache_write_failed",
			slog.String("request_id", GetRequestIDFromContext(ctx)),
			slog.String("package_id", packageID),
			slog.String("error", cacheErr.Error()),
		)
	}
	return jsonResult, len(files), countSkipped(messages), false, "", ""
}

// unscannedReason is the soft, user-facing acknowledgement shown (as a skipped[]
// entry) for a file whose content could not be fully scanned — read error,
// unreadable archive, or an internal check failure. The technical cause stays in
// the server log, keyed by request_id.
const unscannedReason = "The file could not be fully scanned."

// convertScanDiagnostics drains the buffered GlobalLogger diagnostics collected
// during this analysis and splits them by audience: every raw message (which may
// carry absolute paths and OS error text) is logged via slog keyed by
// request_id, and each distinct affected file (identified by the diagnostic's
// Subject display name) yields ONE soft, path-free skip acknowledgement for the
// response. Subject-less diagnostics (CKAN/transport/config notes) are
// log-only. Must be called under analysisMu, before FormatResults reads the
// logger — clearing the buffer here is what keeps warnings[]/errors[] empty in
// server responses.
func (h *Handler) convertScanDiagnostics(ctx context.Context, packageID string) []structs.Message {
	diags := output.GlobalLogger.GetMessages()
	if len(diags) == 0 {
		return nil
	}
	var soft []structs.Message
	seen := make(map[string]struct{}, len(diags))
	for _, d := range diags {
		level := slog.LevelWarn
		switch d.Level {
		case "error":
			level = slog.LevelError
		case "info":
			level = slog.LevelInfo
		}
		h.logger.LogAttrs(ctx, level, "scan_diagnostic",
			slog.String("request_id", GetRequestIDFromContext(ctx)),
			slog.String("package_id", packageID),
			slog.String("subject", d.Subject),
			slog.String("message", d.Message),
		)
		if d.Subject == "" {
			continue
		}
		if _, dup := seen[d.Subject]; dup {
			continue
		}
		seen[d.Subject] = struct{}{}
		soft = append(soft, structs.Message{
			Content:  unscannedReason,
			Source:   structs.File{Name: d.Subject},
			TestName: "Readability",
			Skipped:  true,
			Reason:   unscannedReason,
		})
	}
	output.GlobalLogger.ClearMessages()
	return soft
}

// scrubMessagePaths blanks the local FileStore path on every outgoing message's
// File source. The response identifies files by display name; absolute
// container paths are internal (spec §3). Message values are updated in place;
// the CLI never calls this.
func scrubMessagePaths(messages []structs.Message) {
	for i := range messages {
		if f, ok := messages[i].Source.(structs.File); ok && f.Path != "" {
			f.Path = ""
			messages[i].Source = f
		}
	}
}

// countSkipped counts the check Messages that report a per-FILE skip (spec §6)
// so the analysis_done lifecycle event can record how many files were skipped.
// The repository-scoped "no files to analyse" notice is also Skipped-flagged but
// is not a file skip, so it is excluded from this count.
func countSkipped(messages []structs.Message) int {
	n := 0
	for _, m := range messages {
		if !m.Skipped {
			continue
		}
		if _, isFile := m.Source.(structs.File); isFile {
			n++
		}
	}
	return n
}

// mapCKANError maps the single package_show outcome to a catalogue code and an
// optional dynamic, user-facing message (§3, §5). The message is non-empty only
// for malformed_resource, where it is the collector's authored, safe-to-surface
// text naming the exact resource + package; every other code uses its fixed
// catalogue message (msg == ""). Mapping:
//   - MalformedResourceError (resource missing url_type+url, or an upload
//     missing name/url/size) -> malformed_resource (422) + its message;
//   - ErrResourceUnreadable (a url_type=="upload" file missing/escaping storage)
//     -> resource_unreadable;
//   - a transport/connection failure or a transport-level HTTP 5xx ->
//     ckan_unavailable (CKAN really is unreachable/erroring);
//   - explicit 401 -> invalid_token, 403 -> access_denied;
//   - 429 (CKAN throttling the server) -> ckan_unavailable: an upstream
//     availability condition, not a server fault — no internal_error, no
//     admin alert;
//   - 404 (nonexistent OR private-unauthorized under CKAN's default
//     reveal_private_datasets=false), including a 200+success:false body that
//     resolves to 404 -> package_not_found;
//   - a 200+success:false body with an UNRECOGNISED error.__type (which
//     ckanActionErrorStatus resolves to 500 with StatusFromBody set): CKAN was
//     reachable and answered 200, so this is not "we can't reach CKAN" — it is an
//     unexpected upstream condition mapped to internal_error so the verbose CKAN
//     __type never leaks into the client envelope;
//   - anything else -> internal_error.
func mapCKANError(err error) (code, msg string) {
	var malformed *collectors.MalformedResourceError
	if errors.As(err, &malformed) {
		// The collector authored this message for the end user (resource +
		// package name, no secrets); surface it verbatim.
		return CodeMalformedResource, malformed.Error()
	}
	if errors.Is(err, collectors.ErrResourceUnreadable) {
		return CodeResourceUnreadable, ""
	}
	var ckanErr *collectors.CKANError
	if errors.As(err, &ckanErr) {
		if ckanErr.Transport {
			return CodeCKANUnavailable, ""
		}
		switch ckanErr.StatusCode {
		case http.StatusUnauthorized:
			return CodeInvalidToken, ""
		case http.StatusForbidden:
			return CodeAccessDenied, ""
		case http.StatusNotFound:
			return CodePackageNotFound, ""
		case http.StatusTooManyRequests:
			// CKAN throttling us is an upstream availability condition: the
			// client should retry later, and no admin alert is warranted.
			return CodeCKANUnavailable, ""
		}
		if ckanErr.StatusCode >= 500 {
			// A status derived from a 200+success:false body (unrecognised
			// __type) is an internal mapping gap, NOT a real upstream outage:
			// CKAN was reachable. Only a genuine transport-level HTTP 5xx is
			// ckan_unavailable.
			if ckanErr.StatusFromBody {
				return CodeInternalError, ""
			}
			return CodeCKANUnavailable, ""
		}
		return CodeInternalError, ""
	}
	// An unrecognised non-CKAN collector error: treat as internal.
	return CodeInternalError, ""
}

// logCKANOutcome emits the CKAN-upstream-outcome slog event (spec §8): the HTTP
// status (or the transport-error class) and the call latency, keyed by
// request_id. It never logs the token or the package-id-carrying URL.
func (h *Handler) logCKANOutcome(ctx context.Context, packageID string, err error, latency time.Duration) {
	attrs := []slog.Attr{
		slog.String("request_id", GetRequestIDFromContext(ctx)),
		slog.String("package_id", packageID),
		slog.Int64("latency_ms", latency.Milliseconds()),
	}
	switch {
	case err == nil:
		attrs = append(attrs, slog.Int("ckan_status", http.StatusOK))
	case errors.Is(err, collectors.ErrResourceUnreadable):
		attrs = append(attrs, slog.String("transport_error_class", "resource_unreadable"))
	default:
		var ckanErr *collectors.CKANError
		if errors.As(err, &ckanErr) {
			if ckanErr.Transport {
				attrs = append(attrs, slog.String("transport_error_class", "transport"))
			} else {
				attrs = append(attrs, slog.Int("ckan_status", ckanErr.StatusCode))
			}
		} else {
			attrs = append(attrs, slog.String("transport_error_class", "collector"))
		}
	}
	h.logger.LogAttrs(ctx, slog.LevelInfo, "ckan_upstream_outcome", attrs...)
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
