package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
)

// alertQueueSize bounds the in-memory backlog of pending alert emails. The
// analyze endpoint is rate-limited (global 20/h by default) and server faults
// are rare, so this is never reached in practice; if it ever is, Notify spills
// the alert to a detached send rather than dropping it (every fault must be
// reported).
const alertQueueSize = 256

// alertDialTimeout bounds a single SMTP delivery (dial + handshake + send) so a
// slow/hung relay can never pile up goroutines or block shutdown.
const alertDialTimeout = 10 * time.Second

// alertPayload is the non-secret summary of a server-fault response, mailed to
// the admin list. It deliberately carries NO token, URL, raw CKAN body or
// internal path — only the request_id (so admins can find the full cause/stack
// in the logs) and the already-validated package_id.
type alertPayload struct {
	RequestID string
	Method    string
	Path      string
	PackageID string
	Code      string
	Time      time.Time
}

// alerter emails the admin list when the server returns a server-fault response
// (internal_error, a recovered panic, or resource_unreadable). It is created
// only when [server.smtp] is configured (Host set and To non-empty); otherwise
// newAlerter returns nil and Notify is a no-op. A single worker goroutine
// serializes deliveries so at most one SMTP connection is open at a time.
type alerter struct {
	from   string
	to     []string
	addr   string // host:port
	logger *slog.Logger

	// send performs one delivery. It is a field so tests can substitute a fake;
	// production uses smtpSend (a plain relay, no auth).
	send func(alertPayload) error

	queue     chan alertPayload
	done      chan struct{}
	closeOnce sync.Once
}

// newAlerter builds an alerter from the [server.smtp] config, or returns nil
// when admin alerts are disabled (no SMTP config, no host, or no recipients).
// On success it starts the worker goroutine.
func newAlerter(cfg *config.SMTPConfig, logger *slog.Logger) *alerter {
	if cfg == nil || strings.TrimSpace(cfg.Host) == "" || len(cfg.To) == 0 {
		return nil
	}
	port := cfg.Port
	if port == 0 {
		port = config.DefaultServerSMTPPort
	}
	a := &alerter{
		from:   cfg.From,
		to:     append([]string(nil), cfg.To...),
		addr:   net.JoinHostPort(cfg.Host, strconv.Itoa(port)),
		logger: logger,
		queue:  make(chan alertPayload, alertQueueSize),
		done:   make(chan struct{}),
	}
	a.send = a.smtpSend
	go a.worker()
	return a
}

// worker delivers queued alerts one at a time until the queue is closed.
func (a *alerter) worker() {
	defer close(a.done)
	for p := range a.queue {
		a.deliver(p)
	}
}

// deliver sends one alert and logs the outcome. Delivery failures are logged but
// never propagated: a broken relay must not affect request handling.
func (a *alerter) deliver(p alertPayload) {
	if err := a.send(p); err != nil {
		a.logger.LogAttrs(context.Background(), slog.LevelWarn, "admin_alert_failed",
			slog.String("request_id", p.RequestID),
			slog.String("code", p.Code),
			slog.String("error", err.Error()),
		)
		return
	}
	a.logger.LogAttrs(context.Background(), slog.LevelInfo, "admin_alert_sent",
		slog.String("request_id", p.RequestID),
		slog.String("code", p.Code),
	)
}

// Notify enqueues an alert without blocking the caller (the request goroutine).
// If the worker queue is momentarily full it spills to a detached send rather
// than dropping the alert, because every fault must be reported. Safe to call on
// a nil *alerter (alerts disabled).
func (a *alerter) Notify(p alertPayload) {
	if a == nil {
		return
	}
	select {
	case a.queue <- p:
	default:
		// Backlog full (pathological): deliver out-of-band so nothing is lost.
		a.logger.LogAttrs(context.Background(), slog.LevelWarn, "admin_alert_queue_full",
			slog.String("request_id", p.RequestID),
			slog.String("code", p.Code),
		)
		go a.deliver(p)
	}
}

// Close stops the worker and waits briefly for it to drain. Safe to call on a
// nil *alerter and idempotent.
func (a *alerter) Close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() { close(a.queue) })
	select {
	case <-a.done:
	case <-time.After(alertDialTimeout):
		// Worker is mid-delivery against a slow relay; don't block shutdown.
	}
}

// smtpSend delivers one alert through the configured plain SMTP relay (no auth).
// The whole exchange is bounded by a connection deadline so a hung relay cannot
// block the worker.
func (a *alerter) smtpSend(p alertPayload) error {
	conn, err := net.DialTimeout("tcp", a.addr, alertDialTimeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", a.addr, err)
	}
	// Bound the entire SMTP exchange.
	_ = conn.SetDeadline(time.Now().Add(alertDialTimeout))

	host, _, err := net.SplitHostPort(a.addr)
	if err != nil {
		host = a.addr
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer client.Close()

	if err := client.Mail(a.from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, rcpt := range a.to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", rcpt, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := wc.Write([]byte(a.buildMessage(p))); err != nil {
		wc.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return client.Quit()
}

// crlfStripper removes CR and LF from a field so it can never inject extra
// SMTP headers or message lines (header-injection defence-in-depth).
var crlfStripper = strings.NewReplacer("\r", "", "\n", "")

// buildMessage renders the RFC 5322 alert mail. It contains only non-secret
// fields; the full cause/stack lives in the server logs, keyed by request_id.
// Every interpolated field that is request-derived (method, path, package_id)
// or otherwise dynamic is CR/LF-stripped first: although the routing pins the
// only fault-producing path to a CRLF-free constant today, sanitising here
// keeps a future route or input change from turning this into a live
// header-injection sink.
func (a *alerter) buildMessage(p alertPayload) string {
	code := crlfStripper.Replace(p.Code)
	requestID := crlfStripper.Replace(p.RequestID)
	method := crlfStripper.Replace(p.Method)
	path := crlfStripper.Replace(p.Path)
	packageID := crlfStripper.Replace(p.PackageID)

	var b strings.Builder
	crlf := func(line string) { b.WriteString(line); b.WriteString("\r\n") }

	crlf("From: " + a.from)
	crlf("To: " + strings.Join(a.to, ", "))
	crlf(fmt.Sprintf("Subject: [pc-server] %s (request %s)", code, requestID))
	crlf("Date: " + p.Time.Format(time.RFC1123Z))
	crlf("Content-Type: text/plain; charset=utf-8")
	crlf("")
	crlf("A pc-server request failed with a server-side error.")
	crlf("")
	crlf("code:        " + code)
	crlf("request_id:  " + requestID)
	crlf("time:        " + p.Time.UTC().Format(time.RFC3339))
	crlf("method:      " + method)
	crlf("path:        " + path)
	crlf("package_id:  " + packageID)
	crlf("")
	crlf("The full cause and stack trace are in the server logs, keyed by request_id.")
	return b.String()
}
