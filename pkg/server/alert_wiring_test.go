package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// recordingAlerter returns a live alerter whose send records every delivered
// payload to the returned channel instead of mailing it. The worker is started
// with the fake send already wired (built directly, like newTestAlerter), so
// there is no race with a default smtpSend assignment.
func recordingAlerter(t *testing.T) (*alerter, <-chan alertPayload) {
	t.Helper()
	got := make(chan alertPayload, 8)
	a := &alerter{
		from:   "alerts@example.org",
		to:     []string{"admin@example.org"},
		addr:   "smtp.example.org:25",
		logger: discardLogger(),
		send: func(p alertPayload) error {
			got <- p
			return nil
		},
		queue: make(chan alertPayload, alertQueueSize),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go a.worker()
	t.Cleanup(a.Close)
	return a, got
}

// expectOneAlert asserts exactly one alert arrives on got with the expected
// code, and that no second alert follows within a short settle window.
func expectOneAlert(t *testing.T, got <-chan alertPayload, wantCode string) {
	t.Helper()
	select {
	case p := <-got:
		if p.Code != wantCode {
			t.Errorf("alert code = %q, want %q", p.Code, wantCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected one alert for code %q, got none", wantCode)
	}
	select {
	case p := <-got:
		t.Errorf("expected exactly one alert, got a second: %+v", p)
	case <-time.After(150 * time.Millisecond):
	}
}

// expectNoAlert asserts no alert arrives within a short settle window.
func expectNoAlert(t *testing.T, got <-chan alertPayload) {
	t.Helper()
	select {
	case p := <-got:
		t.Errorf("expected no alert, got %+v", p)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestRenderError_AlertsOnlyOnServerFaults asserts writeError fires exactly one
// admin alert for the server-fault codes (internal_error, resource_unreadable)
// and NONE for non-faults (a 4xx and analysis_timeout). The alert is driven via
// a request whose context carries the alerter, exactly as RequestContext stashes
// it in production.
func TestRenderError_AlertsOnlyOnServerFaults(t *testing.T) {
	alerts := []struct {
		name      string
		code      string
		wantAlert bool
	}{
		{"internal_error fires", CodeInternalError, true},
		{"resource_unreadable fires", CodeResourceUnreadable, true},
		{"missing_package (4xx) does not fire", CodeMissingPackage, false},
		{"analysis_timeout does not fire", CodeAnalysisTimeout, false},
	}
	for _, tc := range alerts {
		t.Run(tc.name, func(t *testing.T) {
			a, got := recordingAlerter(t)

			req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
			req = withRequestContext(req, "REQ-"+tc.code, DefaultContactMessage)
			req = req.WithContext(withAlerter(req.Context(), a))
			rr := httptest.NewRecorder()

			writeError(rr, req, tc.code)

			if tc.wantAlert {
				expectOneAlert(t, got, tc.code)
			} else {
				expectNoAlert(t, got)
			}
		})
	}
}

// TestRenderError_NoAlerterIsNoOp asserts renderError does not panic and simply
// skips alerting when the request context carries no alerter (e.g. a handler
// exercised in isolation). alerterFromContext returns nil; Notify is nil-safe.
func TestRenderError_NoAlerterIsNoOp(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	req = withRequestContext(req, "REQ-NOALERTER", DefaultContactMessage)
	rr := httptest.NewRecorder()

	// Must not panic even though no alerter is in the context.
	writeError(rr, req, CodeInternalError)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

// TestRecover_PanicFiresExactlyOneAlert asserts a panicking downstream handler
// recovered by h.Recover fires exactly one internal_error alert. Recover is the
// outermost middleware, so the alert path depends on Recover stashing h.alerter
// onto the original request before writeError runs. Exactly-once matters: a
// second direct Notify would double-send.
func TestRecover_PanicFiresExactlyOneAlert(t *testing.T) {
	a, got := recordingAlerter(t)

	h := &Handler{logger: discardLogger(), contactMsg: DefaultContactMessage, alerter: a}

	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})
	// Production chain order: Recover wraps RequestContext (which generates the id
	// and also stashes the alerter for the non-panic path).
	chain := h.Recover(h.RequestContext(boom))

	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after recovered panic, got %d", rr.Code)
	}
	expectOneAlert(t, got, CodeInternalError)
}
