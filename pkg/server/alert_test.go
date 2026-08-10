package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
)

// newTestAlerter builds an alerter with the worker started but a caller-supplied
// send already wired, so the fake send is in place BEFORE the worker can read it
// (no race with newAlerter's default a.send = a.smtpSend assignment). It mirrors
// the production struct exactly except for the send substitution. A non-nil
// clock makes the cooldown gate read that variable instead of the wall clock, so
// the window can be crossed without sleeping; it is installed before the worker
// starts. Close is idempotent, so the cleanup is safe next to an explicit Close.
func newTestAlerter(t *testing.T, send func(alertPayload) error, clock *time.Time) *alerter {
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
	if clock != nil {
		a.dedup.now = func() time.Time { return *clock }
	}
	go a.worker()
	t.Cleanup(a.Close)
	return a
}

// newRecordingAlerter is newTestAlerter with a send that records every delivered
// payload to the returned channel instead of mailing it.
func newRecordingAlerter(t *testing.T, clock *time.Time) (*alerter, <-chan alertPayload) {
	t.Helper()
	got := make(chan alertPayload, 8)
	a := newTestAlerter(t, func(p alertPayload) error {
		got <- p
		return nil
	}, clock)
	return a, got
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
	}, nil)

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
	}, nil)

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
	}, nil)

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

// TestAlerter_ResourceUnreadableCooldown pins the noise cap: a package whose
// file is missing from storage faults on EVERY request, so the admin list must
// hear about it once per window - and the next delivered alert must report how
// many occurrences were swallowed meanwhile.
func TestAlerter_ResourceUnreadableCooldown(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	a, got := newRecordingAlerter(t, &now)

	unreadable := func(reqID string) alertPayload {
		return alertPayload{RequestID: reqID, Method: "POST", Path: "/api/v1/analyze",
			PackageID: "broken-pkg", Code: CodeResourceUnreadable, Time: now}
	}

	// First fault delivers immediately, with nothing suppressed yet.
	a.Notify(unreadable("REQ-1"))
	first := receiveAlert(t, got)
	if first.RequestID != "REQ-1" {
		t.Errorf("first delivery = %q, want REQ-1", first.RequestID)
	}
	if first.Suppressed != 0 {
		t.Errorf("first delivery Suppressed = %d, want 0", first.Suppressed)
	}

	// Two more inside the window: both swallowed, nothing delivered.
	now = now.Add(10 * time.Minute)
	a.Notify(unreadable("REQ-2"))
	now = now.Add(10 * time.Minute)
	a.Notify(unreadable("REQ-3"))
	expectNoAlert(t, got)

	// Past the window the next fault delivers again and reports the tally.
	now = now.Add(alertCooldownWindow)
	a.Notify(unreadable("REQ-4"))
	second := receiveAlert(t, got)
	if second.RequestID != "REQ-4" {
		t.Errorf("second delivery = %q, want REQ-4", second.RequestID)
	}
	if second.Suppressed != 2 {
		t.Errorf("second delivery Suppressed = %d, want 2", second.Suppressed)
	}

	// The counter is read AND reset at gate-pass time: the delivery after the
	// following window must not re-report the same two.
	now = now.Add(alertCooldownWindow)
	a.Notify(unreadable("REQ-5"))
	third := receiveAlert(t, got)
	if third.Suppressed != 0 {
		t.Errorf("third delivery Suppressed = %d, want 0 (counter must reset)", third.Suppressed)
	}
}

// TestAlerter_CooldownIsPerPackage asserts the window is keyed on the package,
// so one noisy dataset can never mute a DIFFERENT dataset's first fault. An
// empty package id fails open: it bypasses the gate entirely rather than forming
// one shared anonymous bucket.
func TestAlerter_CooldownIsPerPackage(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	a, got := newRecordingAlerter(t, &now)

	for _, pkg := range []string{"pkg-a", "pkg-b", "", ""} {
		a.Notify(alertPayload{RequestID: "REQ-" + pkg, PackageID: pkg,
			Code: CodeResourceUnreadable, Time: now})
	}

	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		seen[receiveAlert(t, got).PackageID]++
	}
	if seen["pkg-a"] != 1 || seen["pkg-b"] != 1 {
		t.Errorf("expected one delivery per distinct package, got %v", seen)
	}
	if seen[""] != 2 {
		t.Errorf("expected both empty-package faults delivered (no shared bucket), got %d", seen[""])
	}
	expectNoAlert(t, got)
}

// TestAlertDedup_PruneDropsExpiredEntries pins pruneLocked: window-expired
// entries are removed on the next miss-path insert, while in-window entries -
// and expired entries still holding an unreported tally - survive. Guards
// against an edit that evicts live entries (silently disabling the cooldown),
// stops evicting at all, or drops a mid-storm count.
func TestAlertDedup_PruneDropsExpiredEntries(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	d := &alertDedup{now: func() time.Time { return now }}

	if pass, _ := d.allow("pkg-old"); !pass {
		t.Fatal("first pkg-old alert must pass")
	}
	// pkg-tally arms its window and then swallows one occurrence, so it carries
	// a tally nobody has reported yet.
	if pass, _ := d.allow("pkg-tally"); !pass {
		t.Fatal("first pkg-tally alert must pass")
	}
	if pass, _ := d.allow("pkg-tally"); pass {
		t.Fatal("second pkg-tally alert is inside the window and must be suppressed")
	}
	now = now.Add(30 * time.Minute)
	if pass, _ := d.allow("pkg-live"); !pass {
		t.Fatal("first pkg-live alert must pass")
	}

	// 61 minutes after pkg-old armed its window (expired), 31 after pkg-live
	// (still open). The pkg-new miss triggers the prune.
	now = now.Add(31 * time.Minute)
	if pass, _ := d.allow("pkg-new"); !pass {
		t.Fatal("first pkg-new alert must pass")
	}

	d.mu.Lock()
	_, oldKept := d.entries["pkg-old"]
	_, liveKept := d.entries["pkg-live"]
	_, tallyKept := d.entries["pkg-tally"]
	d.mu.Unlock()
	if oldKept {
		t.Error("expired pkg-old entry must be pruned on the miss-path insert")
	}
	if !liveKept {
		t.Error("in-window pkg-live entry must survive pruning")
	}
	if !tallyKept {
		t.Fatal("expired pkg-tally entry holds an unreported tally and must survive pruning")
	}

	if pass, _ := d.allow("pkg-live"); pass {
		t.Error("pkg-live is inside its window and must still be suppressed")
	}
	if pass, _ := d.allow("pkg-old"); !pass {
		t.Error("pkg-old expired (and was pruned); its next alert must pass")
	}
	// The surviving entry must still report the count it was holding.
	pass, suppressed := d.allow("pkg-tally")
	if !pass {
		t.Fatal("pkg-tally expired; its next alert must pass")
	}
	if suppressed != 1 {
		t.Errorf("pkg-tally reported Suppressed = %d, want 1 (the tally must survive the prune)", suppressed)
	}
}

// TestAlerter_SuppressedNotifyLogsLine asserts a swallowed alert is not silent:
// the gate leaves one admin_alert_suppressed record naming the request, the code
// and the muted package, so a storm is visible in the logs long before the next
// delivered alert reports its tally.
func TestAlerter_SuppressedNotifyLogsLine(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	a, got := newRecordingAlerter(t, &now)

	var buf bytes.Buffer
	var mu sync.Mutex
	// Swapped in before any Notify: the worker reads a.logger only after it
	// dequeues, which cannot happen until the first Notify enqueues.
	a.logger = slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))

	unreadable := func(reqID string) alertPayload {
		return alertPayload{RequestID: reqID, Method: "POST", Path: "/api/v1/analyze",
			PackageID: "broken-pkg", Code: CodeResourceUnreadable, Time: now}
	}
	a.Notify(unreadable("REQ-FIRST"))
	receiveAlert(t, got)
	now = now.Add(time.Minute)
	a.Notify(unreadable("REQ-SWALLOWED"))
	expectNoAlert(t, got)

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	for _, want := range []string{
		`"msg":"admin_alert_suppressed"`,
		`"request_id":"REQ-SWALLOWED"`,
		`"code":"` + CodeResourceUnreadable + `"`,
		`"package_id":"broken-pkg"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("suppression log missing %s; got %s", want, out)
		}
	}
}

// TestAlerter_InternalErrorNeverSuppressed pins the hard rule: only
// resource_unreadable is deduplicated. Repeated internal_error alerts for the
// same package - each potentially a DIFFERENT unknown fault - must all be
// delivered.
func TestAlerter_InternalErrorNeverSuppressed(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	a, got := newRecordingAlerter(t, &now)

	const n = 3
	for i := 0; i < n; i++ {
		a.Notify(alertPayload{RequestID: fmt.Sprintf("REQ-%d", i), PackageID: "same-pkg",
			Code: CodeInternalError, Time: now})
	}
	for i := 0; i < n; i++ {
		p := receiveAlert(t, got)
		if p.Suppressed != 0 {
			t.Errorf("internal_error must never carry a suppression count, got %d", p.Suppressed)
		}
	}
	expectNoAlert(t, got)
}

// receiveAlert waits for the next delivered alert.
func receiveAlert(t *testing.T, got <-chan alertPayload) alertPayload {
	t.Helper()
	select {
	case p := <-got:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("expected an alert delivery, got none")
		return alertPayload{}
	}
}

// TestAlerter_BuildMessage_SuppressedLine asserts the suppression tally is
// rendered as exactly one extra line, and only when there is something to
// report (an alert with nothing suppressed must look exactly as before).
func TestAlerter_BuildMessage_SuppressedLine(t *testing.T) {
	a := &alerter{
		from:   "alerts@example.org",
		to:     []string{"admin@example.org"},
		addr:   "smtp.example.org:25",
		logger: discardLogger(),
	}
	base := alertPayload{
		RequestID: "REQ-SUP", Method: "POST", Path: "/api/v1/analyze",
		PackageID: "broken-pkg", Code: CodeResourceUnreadable,
		Time: time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC),
	}

	none := a.buildMessage(base)
	if strings.Contains(none, "suppressed") {
		t.Errorf("no suppression line expected for Suppressed=0; got:\n%s", none)
	}

	base.Suppressed = 7
	some := a.buildMessage(base)
	if !strings.Contains(some, "suppressed:  7 further occurrences since last alert") {
		t.Errorf("missing suppression line; got:\n%s", some)
	}
	if strings.Count(some, "\r\n") != strings.Count(none, "\r\n")+1 {
		t.Errorf("suppression must add exactly one line: none=%d some=%d",
			strings.Count(none, "\r\n"), strings.Count(some, "\r\n"))
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
