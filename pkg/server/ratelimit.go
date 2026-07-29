package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateScope identifies which limiter rejected a request, for the rate_limited
// slog event (§8).
type rateScope string

const (
	scopeGlobal rateScope = "global"
	scopeIP     rateScope = "ip"
)

// globalKey is the single key under which the global (all-clients) counter is
// tracked. It can never collide with an IP key (IP keys are addresses).
const globalKey = "@global"

// rateEntry is one fixed-window counter. window is the clock-hour the count
// belongs to; a request whose hour differs resets count to zero before
// incrementing (fixed hourly window, §4).
type rateEntry struct {
	window time.Time // truncated to the hour
	count  int
	seen   time.Time // last access, for stale-entry eviction
}

// rateLimiter implements the proxy-aware, fixed-hourly-window in-memory limiter
// from §4. It tracks a per-IP counter per key plus a single global counter, all
// in one map guarded by one mutex. There is no token-bucket library and the
// token is never a key. Memory is bounded by maxTrackedKeys with stale-entry
// eviction so key rotation cannot OOM the limiter.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateEntry

	perIPLimit  int
	globalLimit int

	// Cheap budgets for cache-hit requests (main limits × cachedFactor). A hit
	// refunds its main-budget token and is charged here instead; 0 factor
	// disables refunds entirely.
	cachedFactor      int
	cachedPerIPLimit  int
	cachedGlobalLimit int

	maxTrackedKeys int

	// trustProxyHeaders gates whether X-Real-IP is consulted at all; when true
	// it is honored only for connections whose RemoteAddr is within
	// trustedProxies (parsed CIDRs).
	trustProxyHeaders bool
	trustedProxies    []*net.IPNet

	// now is injectable so tests can drive the clock across hour boundaries.
	now func() time.Time
}

// newRateLimiter builds a limiter from the [server] config. The effective caps
// apply the burst factor: limit = perHour × (1 + burstFactor), rounded down,
// with a floor of 1 so a positive per-hour budget always admits at least one
// request (§4). A non-positive per-hour budget disables that scope (limit 0 =
// unlimited).
func newRateLimiter(perIPPerHour, globalPerHour int, burstFactor float64, cachedFactor int, maxTrackedKeys int, trustProxyHeaders bool, trustedProxies []string) *rateLimiter {
	if cachedFactor < 0 {
		cachedFactor = 0
	}
	return &rateLimiter{
		entries:           make(map[string]*rateEntry),
		perIPLimit:        effectiveLimit(perIPPerHour, burstFactor),
		globalLimit:       effectiveLimit(globalPerHour, burstFactor),
		cachedFactor:      cachedFactor,
		cachedPerIPLimit:  effectiveLimit(perIPPerHour*cachedFactor, burstFactor),
		cachedGlobalLimit: effectiveLimit(globalPerHour*cachedFactor, burstFactor),
		maxTrackedKeys:    maxTrackedKeys,
		trustProxyHeaders: trustProxyHeaders,
		trustedProxies:    parseCIDRs(trustedProxies),
		now:               time.Now,
	}
}

// effectiveLimit applies the burst factor to a per-hour budget. A non-positive
// budget yields 0, which the limiter treats as "no limit for this scope".
func effectiveLimit(perHour int, burstFactor float64) int {
	if perHour <= 0 {
		return 0
	}
	if burstFactor < 0 {
		burstFactor = 0
	}
	limit := int(float64(perHour) * (1 + burstFactor))
	if limit < 1 {
		limit = 1
	}
	return limit
}

// parseCIDRs parses a list of CIDR strings, silently dropping any that fail to
// parse (configuration is validated elsewhere; a bad entry simply does not
// grant proxy trust).
func parseCIDRs(cidrs []string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range cidrs {
		if _, ipNet, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			out = append(out, ipNet)
		}
	}
	return out
}

// allow records one request against the given key/scope and reports whether it
// is within the effective limit. retryAfter is the duration until the next
// clock-hour boundary, returned whether or not the request is allowed (the
// caller only uses it on rejection). A scope whose limit is 0 is unlimited.
func (rl *rateLimiter) allow(key string, scope rateScope) (allowed bool, count, limit int, retryAfter time.Duration) {
	now := rl.now()
	window := now.Truncate(time.Hour)
	retryAfter = window.Add(time.Hour).Sub(now)

	switch scope {
	case scopeGlobal:
		limit = rl.globalLimit
	default:
		limit = rl.perIPLimit
	}
	if limit <= 0 {
		// Unlimited scope: never tracked, never rejected.
		return true, 0, 0, retryAfter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	e, ok := rl.entries[key]
	if !ok {
		// Before inserting a new key, enforce the memory bound by evicting
		// stale entries (and, if still full, the single oldest-seen entry).
		rl.evictLocked(window)
		e = &rateEntry{window: window}
		rl.entries[key] = e
	}

	// Fixed-window reset: a new clock-hour zeroes the counter.
	if !e.window.Equal(window) {
		e.window = window
		e.count = 0
	}

	e.seen = now
	e.count++
	count = e.count

	return count <= limit, count, limit, retryAfter
}

// cachedKeySuffix marks the cheap-budget counter keys for cache-hit requests.
// NUL can never appear in an IP-derived key or in globalKey, so a cached key
// cannot collide with a main key.
const cachedKeySuffix = "\x00cached"

// refundCached returns one main-budget token to the per-IP and global counters
// after a response served from the result cache, charging the cheap cached
// budgets instead. A scope whose cached budget for this window is exhausted
// keeps its main-budget charge (the hit then counts at full price), so cache
// traffic stays bounded - every hit still costs one CKAN package_show. Refund
// and re-charge happen under one lock so concurrent hits cannot over-refund.
func (rl *rateLimiter) refundCached(key string) {
	if rl.cachedFactor <= 0 {
		return
	}
	now := rl.now()
	window := now.Truncate(time.Hour)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.refundScopeLocked(key, rl.perIPLimit, rl.cachedPerIPLimit, window, now)
	rl.refundScopeLocked(globalKey, rl.globalLimit, rl.cachedGlobalLimit, window, now)
}

// refundScopeLocked refunds one token for a single scope. No-op when the scope
// is unlimited (nothing was charged), the main entry is missing/stale/zero, or
// the cached budget is spent. Caller holds rl.mu.
func (rl *rateLimiter) refundScopeLocked(key string, mainLimit, cachedLimit int, window, now time.Time) {
	if mainLimit <= 0 {
		return
	}
	main, ok := rl.entries[key]
	if !ok || !main.window.Equal(window) || main.count <= 0 {
		return
	}

	ck := key + cachedKeySuffix
	c, ok := rl.entries[ck]
	if !ok {
		rl.evictLocked(window)
		c = &rateEntry{window: window}
		rl.entries[ck] = c
	}
	if !c.window.Equal(window) {
		c.window = window
		c.count = 0
	}
	if c.count >= cachedLimit {
		return
	}
	c.seen = now
	c.count++
	main.count--
}

// evictLocked bounds the number of tracked keys (§4). It first drops entries
// whose window is older than the current one (stale: their counters would reset
// anyway). If the map is still at capacity it drops the single oldest-seen
// entry to make room for the incoming key. Caller holds rl.mu.
func (rl *rateLimiter) evictLocked(currentWindow time.Time) {
	if rl.maxTrackedKeys <= 0 {
		return
	}

	if len(rl.entries) < rl.maxTrackedKeys {
		return
	}

	// First pass: remove stale (previous-window) entries.
	for k, e := range rl.entries {
		if e.window.Before(currentWindow) {
			delete(rl.entries, k)
		}
	}

	if len(rl.entries) < rl.maxTrackedKeys {
		return
	}

	// Still full of current-window entries: evict the oldest-seen one so the
	// incoming key fits. This is a rare hot-window-with-many-IPs case.
	var oldestKey string
	var oldestSeen time.Time
	for k, e := range rl.entries {
		if oldestKey == "" || e.seen.Before(oldestSeen) {
			oldestKey = k
			oldestSeen = e.seen
		}
	}
	if oldestKey != "" {
		delete(rl.entries, oldestKey)
	}
}

// clientIPKey returns the rate-limit key for a request: the /64 prefix for IPv6
// or the full address for IPv4. The source address is X-Real-IP only when
// trustProxyHeaders is set AND the connection's RemoteAddr is within a trusted
// proxy CIDR; otherwise it is RemoteAddr. A spoofed X-Real-IP from an untrusted
// RemoteAddr is ignored (§4).
func (rl *rateLimiter) clientIPKey(r *http.Request) string {
	ip := rl.clientIP(r)
	return ipKey(ip)
}

// clientIP resolves the effective client IP for r per the proxy-trust policy.
func (rl *rateLimiter) clientIP(r *http.Request) net.IP {
	remoteIP := hostIP(r.RemoteAddr)

	if rl.trustProxyHeaders && rl.isTrustedProxy(remoteIP) {
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			if parsed := net.ParseIP(realIP); parsed != nil {
				return parsed
			}
		}
	}
	return remoteIP
}

// isTrustedProxy reports whether ip falls within any configured trusted-proxy
// CIDR.
func (rl *rateLimiter) isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range rl.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// hostIP extracts the IP from a "host:port" RemoteAddr (or a bare host),
// returning nil if it cannot be parsed.
func hostIP(remoteAddr string) net.IP {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	return net.ParseIP(strings.TrimSpace(host))
}

// ipKey renders an IP as a rate-limit key. IPv6 addresses are keyed on their
// /64 prefix so a single client cannot trivially rotate through its /64 to
// evade the per-IP cap (§4). IPv4 addresses are keyed on the full address. A
// nil/unparseable IP yields a stable sentinel so such requests still share one
// bucket rather than minting unbounded keys.
func ipKey(ip net.IP) string {
	if ip == nil {
		return "@unknown"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	// IPv6: mask to the /64 network prefix.
	masked := ip.Mask(net.CIDRMask(64, 128))
	return masked.String() + "/64"
}

// hashIPKey returns a non-reversible, truncated identifier for a rate-limit key,
// for logging (§8): a request_id-correlated abuse signal that is not the raw IP
// and never the token. It is the first 12 hex chars of SHA-256(key).
func hashIPKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}
