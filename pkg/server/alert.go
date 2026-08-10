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
// the alert to a detached send rather than dropping it (no fault that passes
// the cooldown gate is ever dropped, see alertDedup).
const alertQueueSize = 256

// alertDialTimeout bounds a single SMTP delivery (dial + handshake + send) so a
// slow/hung relay can never pile up goroutines or block shutdown.
const alertDialTimeout = 10 * time.Second

// alertPayload is the non-secret summary of a server-fault response, mailed to
// the admin list. It deliberately carries NO token, URL, raw CKAN body or
// internal path - only the request_id (so admins can find the full cause/stack
// in the logs) and the already-validated package_id.
type alertPayload struct {
	RequestID string
	Method    string
	Path      string
	PackageID string
	Code      string
	Time      time.Time
	// Suppressed is how many further occurrences the cooldown swallowed since
	// the previous delivered alert for this package (0 when none). It is set by
	// Notify at gate-pass time, never by the caller.
	Suppressed int
}

// alertCooldownWindow is how long a gate-passed resource_unreadable alert mutes
// further alerts for the SAME package. A dataset with a missing/dead file in
// storage produces one such fault per request, so without a window a single
// broken package mails the admin list on every retry. One hour is short enough
// that a genuinely new outage is still reported promptly.
const alertCooldownWindow = time.Hour

// dedupEntry is one package's cooldown state: when its last alert was delivered
// and how many occurrences have been swallowed since.
type dedupEntry struct {
	last       time.Time
	suppressed int
}

// alertDedup is the cooldown gate for the ONE noisy fault class,
// resource_unreadable (§7c). It is deliberately its own tiny structure with its
// OWN mutex: alerter.mu carries the delicate "no enqueued alert is ever
// stranded" invariant and must not take on a second duty.
type alertDedup struct {
	mu      sync.Mutex
	entries map[string]*dedupEntry

	// now is injectable so the window test needs no sleep. A nil now means
	// time.Now, so a hand-built alerter (as in tests) still has a working gate.
	now func() time.Time
}

// allow reports whether an alert for key may be delivered now and, when it may,
// how many occurrences were suppressed since the previous delivery. The counter
// is read AND reset at gate-pass time because delivery is asynchronous: a count
// carried past the gate would be double-reported.
func (d *alertDedup) allow(key string) (bool, int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if d.now != nil {
		now = d.now()
	}

	e, ok := d.entries[key]
	if !ok {
		if d.entries == nil {
			d.entries = make(map[string]*dedupEntry)
		}
		d.pruneLocked(now)
		d.entries[key] = &dedupEntry{last: now}
		return true, 0
	}
	if now.Sub(e.last) < alertCooldownWindow {
		e.suppressed++
		return false, 0
	}
	suppressed := e.suppressed
	e.suppressed = 0
	e.last = now
	return true, suppressed
}

// pruneLocked drops entries that are neither muting a package RIGHT NOW nor
// holding an unreported tally, so the map stays bounded by exactly those two
// sets. An expired entry with suppressed == 0 would let its next alert through
// anyway, so forgetting it changes no outcome; an expired entry with a nonzero
// tally is KEPT, because deleting it would discard a mid-storm count that its
// own next fault is about to report. It runs only on the miss path (first fault
// of a package, or its first after an expiry), which is exactly when the map may
// have grown stale. Caller must hold d.mu.
func (d *alertDedup) pruneLocked(now time.Time) {
	for k, e := range d.entries {
		if e.suppressed == 0 && now.Sub(e.last) >= alertCooldownWindow {
			delete(d.entries, k)
		}
	}
}

// alerter emails the admin list when the server returns a server-fault response
// (internal_error, a recovered panic, or resource_unreadable). It is created
// only when [server.smtp] is configured (Host set and To non-empty); otherwise
// newAlerter returns nil and Notify is a no-op. A single worker goroutine
// serializes deliveries so at most one SMTP connection is open at a time.
//
// Reporting invariant: every fault passes the gate, EXCEPT that
// resource_unreadable passes at most once per package per alertCooldownWindow
// (an empty package id fails open: it bypasses the gate rather than sharing an
// anonymous bucket). The window arms at gate-pass, not at delivery: delivery is
// asynchronous and best-effort, so a failed SMTP send does not reopen it.
// Swallowed occurrences are tallied and reported with the next alert that passes
// for that package; a tally with no next alert (storm over, or shutdown) is lost
// by design. Nothing else is ever deduplicated - internal_error, including a
// recovered panic, is an unknown fault and an alerting system must not drop it.
type alerter struct {
	from   string
	to     []string
	addr   string // host:port
	logger *slog.Logger

	// dedup is the resource_unreadable cooldown gate. It is a value (not a
	// pointer) with lazy state so an alerter built field-by-field - as the tests
	// do - always has a working gate.
	dedup alertDedup

	// send performs one delivery. It is a field so tests can substitute a fake;
	// production uses smtpSend (a plain relay, no auth).
	send func(alertPayload) error

	// queue holds pending alerts for the worker. It is NEVER closed: shutdown is
	// signalled by closing stop instead, so a Notify that races a Close can never
	// "send on a closed channel".
	queue chan alertPayload
	// stop is closed by Close to tell the worker to drain and exit. Closing stop
	// (rather than queue) is what makes a post-Close Notify panic-free.
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	// mu guards stopped and makes "observe not-stopped" and "enqueue" a single
	// atomic step w.r.t. Close flipping stopped. Without it, a Notify that saw
	// stop open could still win its enqueue AFTER the worker had drained and
	// exited, stranding the alert in the buffered queue forever (a lost fault).
	//
	// Invariant: every enqueue happens under mu while !stopped, hence strictly
	// before Close sets stopped=true (also under mu) and closes stop. The worker's
	// drain runs after stop is closed and empties the queue, so no enqueued alert
	// is ever missed. Once stopped is true, Notify delivers out-of-band instead of
	// enqueuing. The queue is still never closed, so sending to it never panics.
	mu      sync.Mutex
	stopped bool
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
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	a.send = a.smtpSend
	go a.worker()
	return a
}

// worker delivers queued alerts one at a time until Close signals stop. It reads
// from queue (which is NEVER closed) and watches stop; on stop it drains any
// already-queued alerts without blocking, then exits. Because shutdown is a
// closed stop channel rather than a closed queue, a Notify racing Close can never
// send on a closed channel.
func (a *alerter) worker() {
	defer close(a.done)
	for {
		select {
		case p := <-a.queue:
			a.deliver(p)
		case <-a.stop:
			// Stop requested: drain whatever is already queued, then exit. The
			// default case guarantees this never blocks.
			for {
				select {
				case p := <-a.queue:
					a.deliver(p)
				default:
					return
				}
			}
		}
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
// It is non-blocking, nil-safe (alerts disabled), and never drops a fault other
// than a resource_unreadable repeat inside its package's cooldown window (see
// the alerter reporting invariant for what the gate does and does not cover).
//
// The gate is consulted FIRST, entirely outside the mu critical section below
// (see alertDedup for why it guards itself). A swallowed occurrence leaves an
// admin_alert_suppressed log line, so the tally is observable before the next
// alert reports it.
//
// The "observe not-stopped" and "enqueue" steps are taken together under mu, so
// they are atomic w.r.t. Close flipping stopped (see the alerter.mu invariant).
// While !stopped, the enqueue is guaranteed to land strictly before Close closes
// stop, so the worker's post-stop drain will deliver it; no enqueued alert can be
// stranded in the queue. Once stopped is true, a post-Close Notify (e.g. an
// in-flight request faulting during a graceful-shutdown drain deadline) delivers
// the alert out-of-band on a detached goroutine instead, so nothing is lost. The
// queue is never closed, so the send under mu can never panic. The send is the
// non-blocking form of select, so mu is held only for an instant.
func (a *alerter) Notify(p alertPayload) {
	if a == nil {
		return
	}
	if p.Code == CodeResourceUnreadable && p.PackageID != "" {
		pass, suppressed := a.dedup.allow(p.PackageID)
		if !pass {
			a.logger.LogAttrs(context.Background(), slog.LevelInfo, "admin_alert_suppressed",
				slog.String("request_id", p.RequestID),
				slog.String("code", p.Code),
				slog.String("package_id", p.PackageID),
			)
			return
		}
		p.Suppressed = suppressed
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		// Shutting down: deliver out-of-band so nothing is lost and we never hand
		// work to a worker that may have already stopped reading the queue.
		go a.deliver(p)
		return
	}
	select {
	case a.queue <- p:
		a.mu.Unlock()
	default:
		a.mu.Unlock()
		// Backlog full (pathological): deliver out-of-band so nothing is lost.
		a.logger.LogAttrs(context.Background(), slog.LevelWarn, "admin_alert_queue_full",
			slog.String("request_id", p.RequestID),
			slog.String("code", p.Code),
		)
		go a.deliver(p)
	}
}

// Close signals the worker to drain and exit, then waits briefly for it. Under
// closeOnce it first sets stopped=true under mu and THEN closes stop: setting the
// flag before closing stop is what makes any concurrent Notify either enqueue
// strictly before shutdown (and be drained by the worker) or, once it observes
// stopped, deliver out-of-band - so no alert that passed the cooldown gate is
// ever lost (see the alerter.mu invariant). Shutdown does not touch the gate:
// its state simply dies with the process, and the first fault after a restart
// alerts again. It closes stop (NOT queue), so a Notify that races Close can never
// send on a closed channel. Safe to call on a nil *alerter and idempotent:
// closeOnce guards the flag-set and close, and the bounded wait keeps shutdown
// from blocking on a slow relay.
func (a *alerter) Close() {
	if a == nil {
		return
	}
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.stopped = true
		a.mu.Unlock()
		close(a.stop)
	})
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
// Every interpolated field is CR/LF-stripped first - both the request-derived
// fields (method, path, package_id) and the operator-config From/To addresses
// (already validated by mail.ParseAddress at boot, but stripped here for
// consistency and defence-in-depth). Although the routing pins the only
// fault-producing path to a CRLF-free constant today, sanitising here keeps a
// future route or input change from turning this into a live header-injection
// sink.
func (a *alerter) buildMessage(p alertPayload) string {
	code := crlfStripper.Replace(p.Code)
	requestID := crlfStripper.Replace(p.RequestID)
	method := crlfStripper.Replace(p.Method)
	path := crlfStripper.Replace(p.Path)
	packageID := crlfStripper.Replace(p.PackageID)

	var b strings.Builder
	crlf := func(line string) { b.WriteString(line); b.WriteString("\r\n") }

	crlf("From: " + crlfStripper.Replace(a.from))
	crlf("To: " + crlfStripper.Replace(strings.Join(a.to, ", ")))
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
	if p.Suppressed > 0 {
		// Only resource_unreadable is ever gated, so this line appears only there.
		// The value is an int, so it cannot carry CRLF.
		crlf(fmt.Sprintf("suppressed:  %d further occurrences since last alert", p.Suppressed))
	}
	crlf("")
	crlf("The full cause and stack trace are in the server logs, keyed by request_id.")
	return b.String()
}
