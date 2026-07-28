package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
)

// fixedClock returns a now() that the test can advance.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestLimiter builds a limiter with an injected clock pinned to a known
// instant inside an hour (so the next-boundary math is deterministic).
func newTestLimiter(t *testing.T, perIP, global int, burst float64, maxKeys int, trustProxies bool, cidrs []string) (*rateLimiter, *fixedClock) {
	t.Helper()
	rl := newRateLimiter(perIP, global, burst, maxKeys, trustProxies, cidrs)
	clk := &fixedClock{t: time.Date(2026, 6, 24, 10, 30, 0, 0, time.UTC)}
	rl.now = clk.now
	return rl, clk
}

// TestRateLimiter_PerIPCap: with perIP=4, burst=0.5 the effective cap is 6; the
// 7th request in the window is rejected (§4 acceptance: 429 on N+1).
func TestRateLimiter_PerIPCap(t *testing.T) {
	rl, _ := newTestLimiter(t, 4, 0, 0.5, 1000, false, nil)
	const want = 6
	for i := 1; i <= want; i++ {
		ok, count, limit, _ := rl.allow("1.2.3.4", scopeIP)
		if !ok {
			t.Fatalf("request %d rejected early (count=%d limit=%d)", i, count, limit)
		}
		if limit != want {
			t.Fatalf("effective per-IP limit = %d, want %d", limit, want)
		}
	}
	ok, count, limit, retry := rl.allow("1.2.3.4", scopeIP)
	if ok {
		t.Fatalf("request %d should be rejected (count=%d limit=%d)", want+1, count, limit)
	}
	if retry <= 0 || retry > time.Hour {
		t.Errorf("retryAfter = %v, want (0, 1h]", retry)
	}
}

// TestRateLimiter_GlobalCap: global=20, burst=0.5 -> cap 30; the 31st request
// across all keys is rejected regardless of which IP it came from.
func TestRateLimiter_GlobalCap(t *testing.T) {
	rl, _ := newTestLimiter(t, 0, 20, 0.5, 1000, false, nil)
	const want = 30
	for i := 1; i <= want; i++ {
		ok, _, limit, _ := rl.allow(globalKey, scopeGlobal)
		if !ok {
			t.Fatalf("global request %d rejected early", i)
		}
		if limit != want {
			t.Fatalf("effective global limit = %d, want %d", limit, want)
		}
	}
	if ok, _, _, _ := rl.allow(globalKey, scopeGlobal); ok {
		t.Fatalf("global request %d should be rejected", want+1)
	}
}

// TestRateLimiter_HourBoundaryReset: a full window rejects, then advancing past
// the clock-hour boundary resets the counter and admits again (injected clock).
func TestRateLimiter_HourBoundaryReset(t *testing.T) {
	rl, clk := newTestLimiter(t, 2, 0, 0, 1000, false, nil) // cap = 2
	for i := 1; i <= 2; i++ {
		if ok, _, _, _ := rl.allow("9.9.9.9", scopeIP); !ok {
			t.Fatalf("request %d rejected early", i)
		}
	}
	if ok, _, _, _ := rl.allow("9.9.9.9", scopeIP); ok {
		t.Fatal("3rd request in window should be rejected")
	}

	// Cross the hour boundary (from 10:30 -> 11:01) and the counter resets.
	clk.advance(31 * time.Minute)
	if ok, count, _, _ := rl.allow("9.9.9.9", scopeIP); !ok {
		t.Fatalf("request after hour reset should be allowed (count=%d)", count)
	}
}

// TestRateLimiter_RetryAfterToBoundary: retryAfter is the time to the next hour
// boundary. At 10:30 that is 30 minutes.
func TestRateLimiter_RetryAfterToBoundary(t *testing.T) {
	rl, _ := newTestLimiter(t, 1, 0, 0, 1000, false, nil) // cap = 1
	rl.allow("5.5.5.5", scopeIP)                          // consume the single slot
	_, _, _, retry := rl.allow("5.5.5.5", scopeIP)
	if retry != 30*time.Minute {
		t.Errorf("retryAfter = %v, want 30m", retry)
	}
}

// TestRateLimiter_UnlimitedScope: a non-positive per-hour budget disables the
// scope (limit 0) and never rejects or tracks keys.
func TestRateLimiter_UnlimitedScope(t *testing.T) {
	rl, _ := newTestLimiter(t, 0, 0, 0.5, 1000, false, nil)
	for i := 0; i < 100; i++ {
		if ok, _, limit, _ := rl.allow("7.7.7.7", scopeIP); !ok || limit != 0 {
			t.Fatalf("unlimited scope rejected at i=%d (limit=%d)", i, limit)
		}
	}
	if rl.trackedKeys() != 0 {
		t.Errorf("unlimited scope should track no keys, got %d", rl.trackedKeys())
	}
}

// TestEffectiveLimit covers the burst-factor math and edge cases.
func TestEffectiveLimit(t *testing.T) {
	cases := []struct {
		perHour int
		burst   float64
		want    int
	}{
		{4, 0.5, 6},
		{20, 0.5, 30},
		{4, 0, 4},
		{1, 0.5, 1}, // floor of 1
		{0, 0.5, 0}, // disabled
		{-3, 0.5, 0},
		{10, -1, 10}, // negative burst clamped to 0
	}
	for _, c := range cases {
		if got := effectiveLimit(c.perHour, c.burst); got != c.want {
			t.Errorf("effectiveLimit(%d, %v) = %d, want %d", c.perHour, c.burst, got, c.want)
		}
	}
}

// TestClientIPKey_ProxyTrust: X-Real-IP is honored only when RemoteAddr is in a
// trusted-proxy CIDR; a spoofed header from an untrusted RemoteAddr is ignored
// and the connection address is used (§4).
func TestClientIPKey_ProxyTrust(t *testing.T) {
	rl := newRateLimiter(4, 0, 0.5, 1000, true, []string{"127.0.0.1/32", "10.0.0.0/8"})

	// Trusted proxy: X-Real-IP wins.
	r := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Real-IP", "203.0.113.7")
	if got := rl.clientIPKey(r); got != "203.0.113.7" {
		t.Errorf("trusted proxy: key = %q, want client IP 203.0.113.7", got)
	}

	// Untrusted RemoteAddr with a spoofed X-Real-IP: header ignored, connection
	// address used.
	r2 := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	r2.RemoteAddr = "198.51.100.9:6666"
	r2.Header.Set("X-Real-IP", "203.0.113.7") // spoof attempt
	if got := rl.clientIPKey(r2); got != "198.51.100.9" {
		t.Errorf("untrusted proxy: key = %q, want connection IP 198.51.100.9", got)
	}
}

// TestClientIPKey_ProxyHeadersDisabled: with trustProxyHeaders=false, X-Real-IP
// is never consulted even from an otherwise-trusted address.
func TestClientIPKey_ProxyHeadersDisabled(t *testing.T) {
	rl := newRateLimiter(4, 0, 0.5, 1000, false, []string{"127.0.0.1/32"})
	r := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Real-IP", "203.0.113.7")
	if got := rl.clientIPKey(r); got != "127.0.0.1" {
		t.Errorf("proxy headers disabled: key = %q, want 127.0.0.1", got)
	}
}

// TestIPKey_IPv6On64: two IPv6 addresses sharing a /64 collapse to the same key
// (§4), while a different /64 keys separately. IPv4 keys on the full address.
func TestIPKey_IPv6On64(t *testing.T) {
	a := net.ParseIP("2001:db8:abcd:1234::1")
	b := net.ParseIP("2001:db8:abcd:1234:ffff:ffff:ffff:ffff")
	c := net.ParseIP("2001:db8:abcd:5678::1")

	if ipKey(a) != ipKey(b) {
		t.Errorf("same /64 should share a key: %q vs %q", ipKey(a), ipKey(b))
	}
	if ipKey(a) == ipKey(c) {
		t.Errorf("different /64 should not share a key: %q == %q", ipKey(a), ipKey(c))
	}
	if !strings.HasSuffix(ipKey(a), "/64") {
		t.Errorf("IPv6 key should carry /64 suffix, got %q", ipKey(a))
	}

	v4 := net.ParseIP("192.0.2.55")
	if ipKey(v4) != "192.0.2.55" {
		t.Errorf("IPv4 key = %q, want full address", ipKey(v4))
	}
}

// TestIPKey_IPv6PerIPCap: requests from distinct hosts in the same /64 share the
// per-IP bucket, so the cap counts them together.
func TestIPKey_IPv6PerIPCap(t *testing.T) {
	rl, _ := newTestLimiter(t, 2, 0, 0, 1000, false, nil) // cap = 2
	k1 := ipKey(net.ParseIP("2001:db8::1"))
	k2 := ipKey(net.ParseIP("2001:db8::2"))
	if k1 != k2 {
		t.Fatalf("expected shared /64 key, got %q and %q", k1, k2)
	}
	rl.allow(k1, scopeIP)
	rl.allow(k2, scopeIP)
	if ok, _, _, _ := rl.allow(k1, scopeIP); ok {
		t.Error("3rd request in shared /64 should be rejected")
	}
}

// TestRateLimiter_EvictsStaleEntries: with a small key cap, inserting more keys
// than the cap (after their windows have gone stale) evicts old entries so the
// tracked-key count stays bounded (§4 limiter memory bound).
func TestRateLimiter_EvictsStaleEntries(t *testing.T) {
	rl, clk := newTestLimiter(t, 4, 0, 0.5, 3, false, nil) // maxKeys = 3

	// Fill three keys in the current window.
	for _, k := range []string{"a", "b", "c"} {
		rl.allow(k, scopeIP)
	}
	if got := rl.trackedKeys(); got != 3 {
		t.Fatalf("tracked keys = %d, want 3", got)
	}

	// Advance past the hour boundary so the three become stale, then insert a
	// fourth: eviction must drop the stale ones, keeping the count bounded.
	clk.advance(time.Hour)
	rl.allow("d", scopeIP)
	if got := rl.trackedKeys(); got > 3 {
		t.Errorf("tracked keys = %d, want <= 3 after stale eviction", got)
	}
	if got := rl.trackedKeys(); got < 1 {
		t.Errorf("tracked keys = %d, want >= 1 (the fresh key)", got)
	}
}

// TestRateLimiter_EvictsWhenAllCurrent: when the cap is reached and every entry
// is in the current window, inserting a new key evicts the oldest-seen one so
// the map never grows past the cap.
func TestRateLimiter_EvictsWhenAllCurrent(t *testing.T) {
	rl, clk := newTestLimiter(t, 4, 0, 0.5, 2, false, nil) // maxKeys = 2

	rl.allow("a", scopeIP)
	clk.advance(time.Second)
	rl.allow("b", scopeIP)
	clk.advance(time.Second)
	rl.allow("c", scopeIP) // forces eviction of the oldest-seen ("a")

	if got := rl.trackedKeys(); got > 2 {
		t.Errorf("tracked keys = %d, want <= 2", got)
	}
}

// TestHashIPKey: the logged key is a short hex digest, never the raw IP.
func TestHashIPKey(t *testing.T) {
	const ip = "203.0.113.7"
	h := hashIPKey(ip)
	if h == ip {
		t.Fatal("hash equals raw IP")
	}
	if len(h) != 12 {
		t.Errorf("hash length = %d, want 12", len(h))
	}
	if h != hashIPKey(ip) {
		t.Error("hash not stable")
	}
}

// rateTestHandler builds a handler whose limiter/semaphore come from the given
// [server] config, with a capturing JSON logger.
func rateTestHandler(t *testing.T, srv *config.ServerConfig) (*Handler, *bytes.Buffer, *sync.Mutex) {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(&syncWriter{w: &buf, mu: &mu}, nil))
	h := NewHandler(&config.Config{Server: srv}, Config{}, logger)
	return h, &buf, &mu
}

// TestRateLimitPerIP_429AndRetryAfter: the per-IP middleware returns the
// rate_limited envelope with a Retry-After header once the cap is exceeded, and
// the rate_limited log line carries a hashed key — never the token.
func TestRateLimitPerIP_429AndRetryAfter(t *testing.T) {
	const token = "secret-token-do-not-log"
	srv := &config.ServerConfig{
		PerIPRequestsPerHour: 2,
		BurstFactor:          0, // cap = 2
		MaxTrackedRateKeys:   100,
	}
	h, buf, mu := rateTestHandler(t, srv)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := h.RateLimitPerIP(inner)

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
		req.RemoteAddr = "192.0.2.10:1111"
		req.Header.Set("Authorization", "Bearer "+token)
		req = withRequestContext(req, "REQ-RL", DefaultContactMessage)
		rr := httptest.NewRecorder()
		mw.ServeHTTP(rr, req)
		return rr
	}

	for i := 1; i <= 2; i++ {
		if rr := do(); rr.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, rr.Code)
		}
	}

	rr := do()
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd request: status %d, want 429", rr.Code)
	}
	if ra := rr.Header().Get("Retry-After"); ra == "" {
		t.Error("missing Retry-After header on 429")
	}
	var resp ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if resp.Error.Code != CodeRateLimited {
		t.Errorf("code = %q, want %q", resp.Error.Code, CodeRateLimited)
	}

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, `"rate_limited"`) {
		t.Errorf("expected a rate_limited log event, got: %s", out)
	}
	if strings.Contains(out, token) {
		t.Errorf("rate_limited log leaked the token: %s", out)
	}
	if strings.Contains(out, "192.0.2.10") {
		t.Errorf("rate_limited log leaked the raw IP: %s", out)
	}
	wantKey := hashIPKey("192.0.2.10")
	if !strings.Contains(out, `"key":"`+wantKey+`"`) {
		t.Errorf("rate_limited log missing hashed key %q; got: %s", wantKey, out)
	}
	if !strings.Contains(out, `"scope":"ip"`) {
		t.Errorf("rate_limited log missing scope=ip; got: %s", out)
	}
}

// TestRateLimitGlobal_429: the global middleware rejects once the global cap is
// exceeded, independent of the client IP.
func TestRateLimitGlobal_429(t *testing.T) {
	srv := &config.ServerConfig{
		GlobalRequestsPerHour: 2,
		BurstFactor:           0, // cap = 2
		MaxTrackedRateKeys:    100,
	}
	h, buf, mu := rateTestHandler(t, srv)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := h.RateLimitGlobal(inner)

	do := func(ip string) int {
		req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
		req.RemoteAddr = ip + ":2222"
		req = withRequestContext(req, "REQ-G", DefaultContactMessage)
		rr := httptest.NewRecorder()
		mw.ServeHTTP(rr, req)
		return rr.Code
	}

	// Different IPs, same global counter.
	if c := do("192.0.2.1"); c != http.StatusOK {
		t.Fatalf("req1: %d", c)
	}
	if c := do("192.0.2.2"); c != http.StatusOK {
		t.Fatalf("req2: %d", c)
	}
	if c := do("192.0.2.3"); c != http.StatusTooManyRequests {
		t.Fatalf("req3: %d, want 429", c)
	}

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, `"scope":"global"`) {
		t.Errorf("expected scope=global rate_limited event; got: %s", out)
	}
}

// TestRateLimitOrder_PerIPRejectionDoesNotConsumeGlobal pins the analyze chain
// ordering: per-IP is the OUTER (primary) limit and global is the INNER
// (backstop), so per-IP runs FIRST. Because the fixed-window limiter increments
// its counter even when it rejects (allow() does e.count++ before the cap
// check), a per-IP-over-cap request must NOT reach the global limiter — otherwise
// a single abusive IP, already past its own cap, would burn the shared global
// budget on every rejected request and lock everyone else out.
//
// It drives the two real middlewares wired in production order
// (RateLimitPerIP wraps RateLimitGlobal, so per-IP is outer/first) and asserts
// the global counter is untouched by the per-IP rejections.
func TestRateLimitOrder_PerIPRejectionDoesNotConsumeGlobal(t *testing.T) {
	srv := &config.ServerConfig{
		PerIPRequestsPerHour:  1,  // per-IP cap = 1 (burst 0)
		GlobalRequestsPerHour: 10, // generous global cap so it never trips here
		BurstFactor:           0,
		MaxTrackedRateKeys:    100,
	}
	h, _, _ := rateTestHandler(t, srv)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	// Production order: ExtractToken/handler is innermost; global is the inner
	// limiter, per-IP the outer. Per-IP is therefore checked first.
	chain := h.RateLimitPerIP(h.RateLimitGlobal(inner))

	do := func() int {
		req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
		req.RemoteAddr = "192.0.2.77:3333"
		req = withRequestContext(req, "REQ-ORDER", DefaultContactMessage)
		rr := httptest.NewRecorder()
		chain.ServeHTTP(rr, req)
		return rr.Code
	}

	// First request from the IP is admitted and consumes 1 global slot.
	if c := do(); c != http.StatusOK {
		t.Fatalf("request 1: status %d, want 200", c)
	}
	// The next several requests from the SAME IP are over the per-IP cap and
	// must be rejected by the OUTER per-IP gate, short-circuiting before global.
	const overCap = 8
	for i := 0; i < overCap; i++ {
		if c := do(); c != http.StatusTooManyRequests {
			t.Fatalf("over-cap request %d: status %d, want 429 (per-IP)", i+1, c)
		}
	}

	// Only the single admitted request should have touched the global counter.
	// Probe it directly: with global cap 10 and exactly 1 consumed, the next 9
	// global allows must succeed; a 10th must fail. If the per-IP rejections had
	// leaked into the global counter (old buggy order), fewer than 9 would remain.
	for i := 0; i < 9; i++ {
		if ok, _, _, _ := h.limiter.allow(globalKey, scopeGlobal); !ok {
			t.Fatalf("global slot %d should remain (per-IP rejections must not consume global budget)", i+1)
		}
	}
	if ok, _, _, _ := h.limiter.allow(globalKey, scopeGlobal); ok {
		t.Fatal("global cap should now be exhausted after 10 total global allows")
	}
}

// TestConcurrency_ServiceBusyNoQueueing exercises the single serialization gate
// (concurrency = 1) and its bounded busy-wait (§4/§9):
//
//	(a) a second request that arrives while the slot is busy SUCCEEDS if the
//	    holder finishes within the busy-wait window; and
//	(b) a second request that is still blocked after the busy-wait window gets
//	    the busy response — HTTP 503, envelope code service_busy, and a
//	    Retry-After header.
func TestConcurrency_ServiceBusyNoQueueing(t *testing.T) {
	// Sub-case (a): the waiter proceeds once the holder releases within the
	// busy-wait grace period.
	t.Run("ProceedsIfSlotFreesWithinGrace", func(t *testing.T) {
		srv := &config.ServerConfig{MaxTrackedRateKeys: 100}
		h, _, _ := rateTestHandler(t, srv)
		h.analysisBusyWait = time.Second // generous grace for the holder to finish

		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entered <- struct{}{}
			<-release // hold the single slot until the test releases it
			w.WriteHeader(http.StatusOK)
		})
		mw := h.Concurrency(inner)

		// Occupy the single slot with a blocked holder.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := withRequestContext(httptest.NewRequest("POST", "/api/v1/analyze", nil), "REQ-HOLD", DefaultContactMessage)
			mw.ServeHTTP(httptest.NewRecorder(), req)
		}()
		<-entered // holder is inside the handler, owns the slot

		// A second request arrives while busy; it must WAIT, not reject.
		rr := httptest.NewRecorder()
		waiterDone := make(chan struct{})
		go func() {
			req := withRequestContext(httptest.NewRequest("POST", "/api/v1/analyze", nil), "REQ-WAIT", DefaultContactMessage)
			mw.ServeHTTP(rr, req)
			close(waiterDone)
		}()

		// Prove the waiter is genuinely blocked in the gate before freeing the
		// slot. Without this barrier the scheduler could let the holder release
		// and the waiter take the fast path before ever parking, so an
		// immediate-reject regression (no busy-wait) would still reach 200 and
		// ship green. If the waiter returns here it did NOT wait — fail loudly.
		select {
		case <-waiterDone:
			t.Fatal("waiter returned before the slot was freed (it must WAIT in the gate, not reject)")
		case <-time.After(50 * time.Millisecond):
		}

		// Free the slot well within the busy-wait window; the waiter should now
		// acquire it and run the inner handler to a 200.
		close(release)
		select {
		case <-waiterDone:
		case <-time.After(2 * time.Second):
			t.Fatal("waiter never completed after the slot freed")
		}
		if rr.Code != http.StatusOK {
			t.Errorf("waiter status = %d, want 200 (should proceed once slot freed)", rr.Code)
		}
		wg.Wait()
	})

	// Sub-case (b): the waiter is still blocked after the busy-wait window and
	// must get the busy response. The status and code are asserted
	// UNCONDITIONALLY (not gated behind a successful decode).
	t.Run("RejectsAfterGraceWithBusyEnvelope", func(t *testing.T) {
		srv := &config.ServerConfig{MaxTrackedRateKeys: 100}
		h, _, _ := rateTestHandler(t, srv)
		h.analysisBusyWait = 50 * time.Millisecond // short grace so the test is fast

		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entered <- struct{}{}
			<-release // hold the single slot past the busy-wait window
			w.WriteHeader(http.StatusOK)
		})
		mw := h.Concurrency(inner)

		// Occupy the single slot and keep it occupied past the grace period.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := withRequestContext(httptest.NewRequest("POST", "/api/v1/analyze", nil), "REQ-HOLD", DefaultContactMessage)
			mw.ServeHTTP(httptest.NewRecorder(), req)
		}()
		<-entered

		// The second request waits out the busy-wait window and is rejected.
		req := withRequestContext(httptest.NewRequest("POST", "/api/v1/analyze", nil), "REQ-BUSY", DefaultContactMessage)
		rr := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			mw.ServeHTTP(rr, req)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("busy request never returned (gate did not time out)")
		}

		// Assert the busy response UNCONDITIONALLY.
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("busy request: status %d, want 503", rr.Code)
		}
		if ra := rr.Header().Get("Retry-After"); ra == "" {
			t.Error("busy request: missing Retry-After header on 503")
		}
		var resp ErrorResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode busy envelope: %v", err)
		}
		if resp.Error.Code != CodeServiceBusy {
			t.Errorf("code = %q, want %q", resp.Error.Code, CodeServiceBusy)
		}

		// Release the holder.
		close(release)
		wg.Wait()
	})
}

// TestRateLimit_HealthAndReadyExempt: /health is wired without the rate-limit /
// concurrency gates, so even a tiny global cap never blocks it (§1/§4). It is
// exercised through the fully wired mux from New-style assembly.
func TestRateLimit_HealthAndReadyExempt(t *testing.T) {
	srv := &config.ServerConfig{
		GlobalRequestsPerHour: 1,
		PerIPRequestsPerHour:  1,
		BurstFactor:           0, // cap = 1 everywhere
		MaxTrackedRateKeys:    100,
	}
	h, _, _ := rateTestHandler(t, srv)

	// Reproduce the production route assembly: gates wrap /analyze only.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.Health)
	analyze := http.Handler(ExtractToken(h.Analyze))
	analyze = h.Concurrency(analyze)
	analyze = h.RateLimitPerIP(analyze)
	analyze = h.RateLimitGlobal(analyze)
	mux.Handle("POST /api/v1/analyze", analyze)
	srvHandler := h.RequestContext(h.AccessLog(mux))

	// Hammer /health well past the global cap of 1; it must always be 200.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/health", nil)
		req.RemoteAddr = "192.0.2.50:9999"
		rr := httptest.NewRecorder()
		srvHandler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("/health request %d: status %d, want 200 (should be exempt)", i, rr.Code)
		}
	}
}

// trackedKeys returns the current number of tracked keys (test helper).
func (rl *rateLimiter) trackedKeys() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.entries)
}
