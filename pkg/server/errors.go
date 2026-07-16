package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// Error codes for the fixed error catalogue (§3). Every failure rendered to a
// client uses exactly one of these codes.
const (
	CodeMissingPackage     = "missing_package"
	CodeInvalidPackageName = "invalid_package_name"
	CodeInvalidRequest     = "invalid_request"
	CodeInvalidToken       = "invalid_token"
	CodeAccessDenied       = "access_denied"
	CodePackageNotFound    = "package_not_found"
	CodeRateLimited        = "rate_limited"
	CodeServiceBusy        = "service_busy"
	CodeServiceNotReady    = "service_not_ready"
	CodeServerRestarting   = "server_restarting"
	CodeCKANUnavailable    = "ckan_unavailable"
	CodeMalformedResource  = "malformed_resource"
	CodeAnalysisTimeout    = "analysis_timeout"
	CodeResourceUnreadable = "resource_unreadable"
	CodeInternalError      = "internal_error"
)

// catalogueEntry is a fixed mapping from an error code to its HTTP status and
// non-technical, English, client-facing message.
type catalogueEntry struct {
	Status  int
	Message string
}

// errorCatalogue is the single source of truth for client-facing error
// responses (§3). Messages are intentionally non-technical: raw errors, CKAN
// bodies, URLs and file paths are kept out and logged instead.
var errorCatalogue = map[string]catalogueEntry{
	CodeMissingPackage:     {http.StatusBadRequest, "Please provide a dataset name."},
	CodeInvalidPackageName: {http.StatusBadRequest, "That dataset name contains characters CKAN doesn't allow. Please check the name and try again."},
	CodeInvalidRequest:     {http.StatusBadRequest, "Your request was malformed. Please check it and try again."},
	CodeInvalidToken:       {http.StatusUnauthorized, "The access token wasn't accepted. Please check that it's correct and hasn't expired."},
	CodeAccessDenied:       {http.StatusForbidden, "Your token doesn't have permission to read this dataset."},
	CodePackageNotFound:    {http.StatusNotFound, "We couldn't find a dataset with that name. Either the name is misspelled, or it's private — make it public in CKAN, or provide your access token to that package."},
	CodeRateLimited:        {http.StatusTooManyRequests, "You've reached the limit of analyses for this hour. Please try again later."},
	CodeServiceBusy:        {http.StatusServiceUnavailable, "The analysis service is busy right now. Please try again in a minute. If this keeps happening, contact us."},
	CodeServiceNotReady:    {http.StatusServiceUnavailable, "The service isn't ready yet (the data repository or storage is unavailable). Please try again shortly."},
	CodeServerRestarting:   {http.StatusServiceUnavailable, "The service is restarting. Please try again in a moment."},
	CodeCKANUnavailable:    {http.StatusBadGateway, "We can't reach the data repository right now. This is usually temporary — please try again shortly."},
	CodeMalformedResource:  {http.StatusUnprocessableEntity, "A resource in this dataset is malformed and can't be processed. Please check the dataset and try again."},
	CodeAnalysisTimeout:    {http.StatusGatewayTimeout, "The analysis took too long and was stopped. Please try again; if it keeps happening, contact us."},
	CodeResourceUnreadable: {http.StatusInternalServerError, "A file in this dataset couldn't be read from storage."},
	// internal_error's catalogue message is never rendered verbatim: renderError
	// always rebuilds it with the live request_id. Only the Status is read.
	CodeInternalError: {http.StatusInternalServerError, "Something went wrong on our side."},
}

// ErrorBody is the nested payload of the error envelope (§3).
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Contact   string `json:"contact,omitempty"`
	RequestID string `json:"request_id"`
}

// ErrorResponse is the single error envelope returned for every failure.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// writeError renders the error envelope for the given catalogue code. The
// request_id is taken from the request context (set by the request-id
// middleware) and echoed in the X-Request-Id header. The contact suffix is
// included for every code except internal_error, which instead cites the
// request_id in its message.
func writeError(w http.ResponseWriter, r *http.Request, code string) {
	renderError(w, r, code, "")
}

// writeErrorMessage renders the envelope for code but with a dynamic,
// already-user-facing message in place of the catalogue default. It is used for
// malformed_resource, whose message is authored by the collector and names the
// exact resource + package the user must fix (spec §3). The override is safe to
// surface verbatim: it carries no token, URL or internal path. The override is
// ignored for internal_error, which always cites the request_id instead.
func writeErrorMessage(w http.ResponseWriter, r *http.Request, code, message string) {
	renderError(w, r, code, message)
}

// renderError is the single envelope renderer shared by writeError and
// writeErrorMessage: it resolves the catalogue entry (unknown codes fall back
// to internal_error). When messageOverride is non-empty it replaces the
// catalogue message (except for internal_error, which always cites the
// request_id and omits contact).
func renderError(w http.ResponseWriter, r *http.Request, code string, messageOverride string) {
	entry, ok := errorCatalogue[code]
	if !ok {
		code = CodeInternalError
		entry = errorCatalogue[CodeInternalError]
	}
	status := entry.Status

	requestID := GetRequestID(r)
	if requestID == "" {
		// The Recover middleware is the OUTERMOST middleware (spec §9), so its
		// deferred handler still holds the ORIGINAL request whose context predates
		// RequestContext and therefore carries no request_id. RequestContext does,
		// however, set the X-Request-Id response header on the shared
		// ResponseWriter before calling downstream. Fall back to that header so the
		// recovered internal_error envelope still cites a valid id (spec §3:
		// request_id present and equals X-Request-Id).
		requestID = w.Header().Get("X-Request-Id")
	}

	message := entry.Message
	switch {
	case code == CodeInternalError:
		// internal_error cites the request_id in its message and omits contact.
		message = "Something went wrong on our side. Please quote reference " + requestID + " if you contact us."
	case messageOverride != "":
		message = messageOverride
	}

	body := ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	}
	if code != CodeInternalError {
		body.Contact = contactMessage(r)
	}

	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: body})

	// After the response is written, email the admin list for server-fault
	// responses ONLY (internal_error — including a recovered panic — and
	// resource_unreadable). 4xx and the other 5xx codes (analysis_timeout,
	// ckan_unavailable, service_busy, ...) are not server faults and never alert.
	// Notify is non-blocking and nil-safe (a nil alerter = alerts disabled).
	if code == CodeInternalError || code == CodeResourceUnreadable {
		a := alerterFromContext(r.Context())
		a.Notify(alertPayload{
			RequestID: requestID,
			Method:    r.Method,
			Path:      r.URL.Path,
			PackageID: getPackageID(r),
			Code:      code,
			Time:      time.Now(),
		})
	}
}
