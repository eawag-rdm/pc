package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// readinessTTL is how long a /ready probe result is cached (§1). Load balancers
// and Docker healthchecks poll /ready frequently; caching bounds the upstream
// load (one CKAN round-trip + one stat per TTL) without making readiness stale.
const readinessTTL = 5 * time.Second

// readinessProbeTimeout caps a single CKAN reachability probe so a hung CKAN
// cannot block the /ready handler past the cache refresh.
const readinessProbeTimeout = 5 * time.Second

// readinessChecker computes and caches the server's readiness (§1): CKAN API
// reachable AND the storage mount readable. The result is cached for
// readinessTTL so frequent health-check polling does not hammer CKAN. The clock
// and the probe function are injectable so tests can drive caching and the
// not-ready path without real I/O.
type readinessChecker struct {
	mu        sync.Mutex
	ready     bool
	checkedAt time.Time
	hasResult bool
	// inFlight is set while a single goroutine is running the (unlocked) probe on
	// behalf of all callers. It is the singleflight guard that prevents a
	// lock-convoy / thundering herd on cache expiry: only the goroutine that flips
	// it false->true probes; concurrent callers return the last known verdict
	// instead of piling up behind the blocking probe.
	inFlight bool

	// ttl is the cache lifetime; now is the injectable clock.
	ttl time.Duration
	now func() time.Time

	// probe performs one (uncached) readiness evaluation. It is a field so tests
	// can substitute a deterministic probe.
	probe func(ctx context.Context) bool
}

// newReadinessChecker builds the production readiness checker for the given
// handler: it probes the configured CKAN URL and storage mount.
func newReadinessChecker(h *Handler) *readinessChecker {
	rc := &readinessChecker{
		ttl: readinessTTL,
		now: time.Now,
	}
	rc.probe = func(ctx context.Context) bool {
		return probeCKAN(ctx, h.ckanURL(), h.verifyTLS()) && probeStorage(h.storagePath())
	}
	return rc
}

// isReady returns the (possibly cached) readiness verdict. A fresh cached result
// is returned without re-probing. On expiry, exactly ONE caller runs the
// (blocking) probe while holding NO lock; every other concurrent caller returns
// the last known verdict immediately. The lock is held only for the brief
// read-the-cache / publish-the-verdict windows, never across the probe — so a
// burst of /ready polls hitting an expired cache cannot form a lock-convoy
// behind the CKAN round-trip + storage stat.
//
// The probe deliberately runs under a fresh context.Background() rather than the
// caller's request context: the readiness of the instance is a property of CKAN
// and the storage mount, not of any single poller's connection. If the probe
// inherited the request context, an LB/healthcheck that closes its connection
// (cancelling r.Context()) before the probe finished would make the probe fail
// and poison the cache with ready=false for the full TTL — flapping the
// instance out of rotation. probeCKAN applies its own readinessProbeTimeout, so
// dropping the caller's deadline does not let a hung CKAN block indefinitely.
func (rc *readinessChecker) isReady() bool {
	rc.mu.Lock()
	now := rc.now()
	fresh := rc.hasResult && now.Sub(rc.checkedAt) < rc.ttl
	if fresh || rc.inFlight {
		// Either the cache is fresh, or another goroutine is already refreshing it.
		// In both cases return the last known verdict without blocking. Before the
		// very first probe completes (hasResult == false) this returns the zero
		// value (not ready), which is the correct conservative default.
		ready := rc.ready
		rc.mu.Unlock()
		return ready
	}

	// We are the elected prober: run the probe with the lock released so other
	// /ready callers are never serialized behind it.
	rc.inFlight = true
	rc.mu.Unlock()

	verdict := rc.probe(context.Background())

	rc.mu.Lock()
	rc.ready = verdict
	rc.checkedAt = rc.now()
	rc.hasResult = true
	rc.inFlight = false
	rc.mu.Unlock()
	return verdict
}

// probeCKAN reports whether the CKAN Action API is reachable. It issues a short,
// tokenless GET to status_show (a public, cheap action) and treats any HTTP
// response as "reachable" — readiness only needs the repository to answer, not a
// specific status. A transport error (DNS / connection refused / timeout) means
// not reachable. An empty URL is treated as not reachable. It never logs the URL
// or any secret.
func probeCKAN(ctx context.Context, ckanURL string, verifyTLS bool) bool {
	if ckanURL == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	defer cancel()

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifyTLS},
	}
	// The transport is local to this probe; close its idle keep-alive connection
	// to CKAN when we return so it is not leaked until GC reclaims the transport.
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ckanURL+"/api/3/action/status_show", nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Any HTTP response means CKAN is up enough to answer. Readiness does not
	// require a 200 from status_show.
	return true
}

// probeStorage reports whether the configured CKAN storage mount is readable
// (§1). An empty path is treated as not ready (the server cannot read resource
// files). The path must exist, be a directory, and be listable.
func probeStorage(storagePath string) bool {
	if storagePath == "" {
		return false
	}
	info, err := os.Stat(storagePath)
	if err != nil || !info.IsDir() {
		return false
	}
	// Confirm the directory is actually readable (a stale NFS mount can stat but
	// fail to open).
	f, err := os.Open(storagePath)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
		// An empty but readable directory returns io.EOF, which is fine.
		return false
	}
	return true
}

// ckanURL returns the CKAN base URL from the collector config (single source).
func (h *Handler) ckanURL() string {
	return h.serverCfg.GetCKANBaseURL(h.pcConfig)
}

// verifyTLS resolves the TLS-verification policy for CKAN calls.
func (h *Handler) verifyTLS() bool {
	return h.serverCfg.GetVerifyTLS(h.pcConfig)
}

// storagePath returns the configured CKAN storage mount path, or "" if unset.
func (h *Handler) storagePath() string {
	if h.pcConfig == nil {
		return ""
	}
	if cc, ok := h.pcConfig.Collectors["CkanCollector"]; ok {
		if p, ok := cc.Attrs["ckan_storage_path"].(string); ok {
			return p
		}
	}
	return ""
}
