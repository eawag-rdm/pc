package server

import (
	"encoding/json"
	"net/http"
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
	CodeCKANUnavailable    = "ckan_unavailable"
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
	CodeCKANUnavailable:    {http.StatusBadGateway, "We can't reach the data repository right now. This is usually temporary — please try again shortly."},
	CodeResourceUnreadable: {http.StatusInternalServerError, "A file in this dataset couldn't be read from storage."},
	CodeInternalError:      {http.StatusInternalServerError, "Something went wrong on our side. Please quote reference {request_id} if you contact us."},
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
	entry, ok := errorCatalogue[code]
	if !ok {
		code = CodeInternalError
		entry = errorCatalogue[CodeInternalError]
	}
	writeErrorStatus(w, r, code, entry.Status)
}

// writeErrorStatus renders the same envelope as writeError but with an explicit
// HTTP status, overriding the catalogue default. It exists for the request
// timeout path (spec §2), which reuses the ckan_unavailable message but must
// answer 504 Gateway Timeout rather than the catalogue's 502.
func writeErrorStatus(w http.ResponseWriter, r *http.Request, code string, status int) {
	entry, ok := errorCatalogue[code]
	if !ok {
		code = CodeInternalError
		entry = errorCatalogue[CodeInternalError]
		status = entry.Status
	}

	requestID := GetRequestID(r)

	message := entry.Message
	if code == CodeInternalError {
		// internal_error cites the request_id in its message and omits contact.
		message = "Something went wrong on our side. Please quote reference " + requestID + " if you contact us."
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
}
