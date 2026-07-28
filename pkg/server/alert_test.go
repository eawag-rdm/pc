package server

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
)

// newTestAlerter builds an alerter with the worker started but a caller-supplied
// send already wired, so the fake send is in place BEFORE the worker can read it
// (no race with newAlerter's default a.send = a.smtpSend assignment). It mirrors
// the production struct exactly except for the send substitution.
func newTestAlerter(t *testing.T, send func(alertPayload) error) *alerter {
	t.Helper()
	a := &alerter{
		from:   "alerts@example.org",
		to:     []string{"admin@example.org"},
		addr:   "smtp.example.org:25",
		logger: discardLogger(),
		send:   send,
		queue:  make(chan alertPayload, alertQueueSize),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go a.worker()
	return a
}

// TestNewAlerter_DisabledReturnsNil asserts admin alerts are disabled (nil
// alerter) for every "not configured" shape: nil cfg, empty host, or host set
// but no recipients.
func TestNewAlerter_DisabledReturnsNil(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.SMTPConfig
	}{
		{"nil cfg", nil},
		{"empty host", &config.SMTPConfig{Host: "", From: "a@b.c", To: []string{"x@y.z"}}},
		{"whitespace host", &config.SMTPConfig{Host: "   ", From: "a@b.c", To: []string{"x@y.z"}}},
		{"host but no recipients", &config.SMTPConfig{Host: "smtp.example.org", From: "a@b.c", To: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if a := newAlerter(tc.cfg, discardLogger()); a != nil {
				t.Errorf("expected nil alerter (alerts disabled) for %q, got %v", tc.name, a)
			}
		})
	}
}

// TestNewAlerter_EnabledStartsWorker asserts a valid config produces a live
// alerter that delivers an enqueued payload to its send, carrying the exact
// non-secret fields. The fake send is installed AFTER newAlerter returns but
// BEFORE Notify enqueues anything; the worker only reads a.send when it dequeues,
// which cannot happen until we Notify, so there is no race (and we run -race).
func TestNewAlerter_EnabledStartsWorker(t *testing.T) {
	a := newAlerter(&config.SMTPConfig{
		Host: "smtp.example.org",
		Port: 25,
		From: "alerts@example.org",
		To:   []string{"admin@example.org"},
	}, discardLogger())
	if a == nil {
		t.Fatal("expected a non-nil alerter for a valid config")
	}
	defer a.Close()

	got := make(chan alertPayload, 1)
	a.send = func(p alertPayload) error {
		got <- p
		return nil
	}

	want := alertPayload{RequestID: "REQ-1", Method: "POST", Path: "/api/v1/analyze", PackageID: "pkg-1", Code: CodeInternalError, Time: time.Now()}
	a.Notify(want)

	select {
	case p := <-got:
		if p.RequestID != want.RequestID {
			t.Errorf("RequestID = %q, want %q", p.RequestID, want.RequestID)
		}
		if p.Code != want.Code {
			t.Errorf("Code = %q, want %q", p.Code, want.Code)
		}
		if p.PackageID != want.PackageID {
			t.Errorf("PackageID = %q, want %q", p.PackageID, want.PackageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("alert was never delivered to send")
	}
}

// TestAlerter_NotifyDeliversPayload asserts Notify hands the exact payload to
// the (fake) send via the worker, and Close drains it.
func TestAlerter_NotifyDeliversPayload(t *testing.T) {
	got := make(chan alertPayload, 1)
	a := newTestAlerter(t, func(p alertPayload) error {
		got <- p
		return nil
	})

	want := alertPayload{RequestID: "REQ-42", Method: "GET", Path: "/x", PackageID: "the-pkg", Code: CodeResourceUnreadable, Time: time.Now()}
	a.Notify(want)

	select {
	case p := <-got:
		if p.RequestID != "REQ-42" || p.Code != CodeResourceUnreadable || p.PackageID != "the-pkg" {
			t.Errorf("delivered payload mismatch: got %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("payload was never delivered")
	}

	a.Close()
}

// TestAlerter_CloseDrainsAndIsIdempotent asserts Close waits for the worker to
// finish in-flight work and is safe to call twice (no double-close panic).
func TestAlerter_CloseDrainsAndIsIdempotent(t *testing.T) {
	var delivered int
	done := make(chan struct{}, 4)
	a := newTestAlerter(t, func(p alertPayload) error {
		delivered++
		done <- struct{}{}
		return nil
	})

	a.Notify(alertPayload{RequestID: "REQ-A", Code: CodeInternalError})
	a.Notify(alertPayload{RequestID: "REQ-B", Code: CodeInternalError})

	// Wait for both deliveries so the count is deterministic before Close.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("alert was never delivered")
		}
	}

	a.Close()
	a.Close() // idempotent: a second Close must not panic on a re-closed channel.

	if delivered != 2 {
		t.Errorf("expected 2 deliveries, got %d", delivered)
	}
}

// TestAlerter_NilSafe asserts Notify and Close are no-ops on a nil *alerter
// (alerts disabled), so callers never need a nil check.
func TestAlerter_NilSafe(t *testing.T) {
	var a *alerter // nil
	// Must not panic.
	a.Notify(alertPayload{RequestID: "REQ-NIL", Code: CodeInternalError})
	a.Close()
}

// TestAlerter_NotifyAfterCloseNoPanic asserts the shutdown-safety contract: a
// Notify that lands AFTER Close (e.g. an in-flight request faulting during a
// graceful-shutdown drain deadline) must never panic with "send on closed
// channel", and must still deliver the fault out-of-band (every fault is
// reported). The queue is never closed; Close only closes stop.
func TestAlerter_NotifyAfterCloseNoPanic(t *testing.T) {
	var mu sync.Mutex
	var got []alertPayload
	delivered := make(chan struct{}, 1)
	a := newTestAlerter(t, func(p alertPayload) error {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		select {
		case delivered <- struct{}{}:
		default:
		}
		return nil
	})

	// Close first; then Notify must not panic on the (now-stopped) worker.
	a.Close()

	want := alertPayload{RequestID: "REQ-AFTER-CLOSE", Code: CodeInternalError, Time: time.Now()}
	a.Notify(want) // must not panic

	// The fault must still be reported via the out-of-band (detached) path.
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("post-Close Notify did not deliver the fault out-of-band")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].RequestID != want.RequestID {
		t.Errorf("out-of-band delivery mismatch: got %+v, want one %q", got, want.RequestID)
	}
}

// TestAlerter_BuildMessage asserts the rendered mail carries the documented
// non-secret fields and required RFC 5322 headers with CRLF line endings, and
// that it leaks nothing it was never given.
func TestAlerter_BuildMessage(t *testing.T) {
	a := &alerter{
		from:   "alerts@example.org",
		to:     []string{"admin1@example.org", "admin2@example.org"},
		addr:   "smtp.example.org:25",
		logger: discardLogger(),
	}
	p := alertPayload{
		RequestID: "REQ-MSG",
		Method:    "POST",
		Path:      "/api/v1/analyze",
		PackageID: "msg-pkg",
		Code:      CodeInternalError,
		Time:      time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
	}
	msg := a.buildMessage(p)

	// Required headers.
	for _, h := range []string{"From: ", "To: ", "Subject: ", "Date: "} {
		if !strings.Contains(msg, h) {
			t.Errorf("message missing %q header; got:\n%s", h, msg)
		}
	}
	// CRLF line endings (and not bare LF without CR).
	if !strings.Contains(msg, "\r\n") {
		t.Error("message must use CRLF line endings")
	}
	if strings.Contains(strings.ReplaceAll(msg, "\r\n", ""), "\n") {
		t.Error("message contains a bare LF not preceded by CR")
	}

	// The documented fields must all be present.
	for _, want := range []string{
		p.Code, p.RequestID, p.PackageID, p.Method, p.Path,
		"code:", "request_id:", "package_id:", a.from, a.to[0], a.to[1],
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing expected content %q; got:\n%s", want, msg)
		}
	}

	// It must NOT carry any secret: no token field and no Authorization header.
	for _, bad := range []string{"token", "Authorization", "Bearer"} {
		if strings.Contains(msg, bad) {
			t.Errorf("message unexpectedly contains %q; got:\n%s", bad, msg)
		}
	}
}

// TestAlerter_BuildMessage_SanitizesCRLF locks in the header-injection defence:
// CR/LF embedded in any request-derived field must be stripped so it cannot
// inject extra SMTP headers or recipients into the mail. The number of CRLF line
// breaks must equal the fixed template's, regardless of malicious input.
func TestAlerter_BuildMessage_SanitizesCRLF(t *testing.T) {
	a := &alerter{
		from:   "alerts@example.org",
		to:     []string{"admin@example.org"},
		addr:   "smtp.example.org:25",
		logger: discardLogger(),
	}
	clean := a.buildMessage(alertPayload{
		RequestID: "REQ-CLEAN", Method: "POST", Path: "/api/v1/analyze",
		PackageID: "pkg", Code: CodeInternalError, Time: time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
	})
	dirty := a.buildMessage(alertPayload{
		RequestID: "REQ\r\nBcc: evil@x.com",
		Method:    "POST\r\nX-Injected: 1",
		Path:      "/api/v1/analyze\r\nBcc: evil2@x.com",
		PackageID: "pkg\r\nSubject: hijacked",
		Code:      CodeInternalError,
		Time:      time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
	})

	// Injection = NEW line breaks. The flattened text may still appear as a
	// substring on its own field line (harmless); what must never happen is a
	// field breaking onto a fresh line that reads as an injected header.
	if strings.Count(dirty, "\r\n") != strings.Count(clean, "\r\n") {
		t.Errorf("CRLF in fields injected extra lines: clean=%d dirty=%d breaks\n%s",
			strings.Count(clean, "\r\n"), strings.Count(dirty, "\r\n"), dirty)
	}
	for _, line := range strings.Split(dirty, "\r\n") {
		for _, bad := range []string{"Bcc:", "X-Injected:", "Subject: hijacked"} {
			if strings.HasPrefix(line, bad) {
				t.Errorf("injected header line surfaced: %q", line)
			}
		}
	}
}
