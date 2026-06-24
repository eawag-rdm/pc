package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/config"
)

// readyPCConfig builds a PC config pointing the CkanCollector at ckanURL and the
// given storage root.
func readyPCConfig(ckanURL, storagePath string) *config.Config {
	return &config.Config{
		Server: &config.ServerConfig{ContactMessage: DefaultContactMessage},
		Collectors: map[string]*config.CollectorConfig{
			"CkanCollector": {
				Attrs: map[string]interface{}{
					"url":               ckanURL,
					"verify":            false,
					"ckan_storage_path": storagePath,
				},
			},
		},
	}
}

// TestReady_Healthy asserts /ready returns 200 when CKAN answers and the storage
// mount is readable.
func TestReady_Healthy(t *testing.T) {
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer ckan.Close()

	storage := t.TempDir()
	handler := NewHandler(readyPCConfig(ckan.URL, storage), Config{}, discardLogger())

	req := httptest.NewRequest("GET", "/ready", nil)
	req = withRequestContext(req, "REQ-READY", DefaultContactMessage)
	rr := httptest.NewRecorder()
	handler.Ready(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 ready, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestReady_CKANDown asserts /ready returns service_not_ready (503) when CKAN is
// unreachable, even though the storage mount is fine.
func TestReady_CKANDown(t *testing.T) {
	// A server we immediately close: connections are refused.
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ckan.URL
	ckan.Close()

	storage := t.TempDir()
	handler := NewHandler(readyPCConfig(url, storage), Config{}, discardLogger())

	req := httptest.NewRequest("GET", "/ready", nil)
	req = withRequestContext(req, "REQ-NOTREADY", DefaultContactMessage)
	rr := httptest.NewRecorder()
	handler.Ready(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeServiceNotReady {
		t.Errorf("expected %q, got %q", CodeServiceNotReady, resp.Error.Code)
	}
}

// TestReady_MountMissing asserts /ready returns service_not_ready (503) when the
// storage mount is missing/unreadable, even though CKAN answers.
func TestReady_MountMissing(t *testing.T) {
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ckan.Close()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	handler := NewHandler(readyPCConfig(ckan.URL, missing), Config{}, discardLogger())

	req := httptest.NewRequest("GET", "/ready", nil)
	req = withRequestContext(req, "REQ-NOMOUNT", DefaultContactMessage)
	rr := httptest.NewRecorder()
	handler.Ready(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
	resp := decodeEnvelope(t, rr)
	if resp.Error.Code != CodeServiceNotReady {
		t.Errorf("expected %q, got %q", CodeServiceNotReady, resp.Error.Code)
	}
}

// TestReady_CancelledRequestContextDoesNotPoisonCache is the regression guard for
// the cache-poisoning bug: a /ready call whose request context is already
// cancelled (e.g. an LB/Docker healthcheck that closed its connection before its
// sub-5s timeout) must still report ready when CKAN + mount are up, and must not
// cache a not-ready verdict that flaps the next poller to 503.
func TestReady_CancelledRequestContextDoesNotPoisonCache(t *testing.T) {
	ckan := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer ckan.Close()

	storage := t.TempDir()
	handler := NewHandler(readyPCConfig(ckan.URL, storage), Config{}, discardLogger())

	// A request whose context is already cancelled, mirroring an http.Server that
	// cancelled r.Context() after the poller disconnected.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/ready", nil).WithContext(ctx)
	req = withRequestContext(req, "REQ-CANCELLED", DefaultContactMessage)
	rr := httptest.NewRecorder()
	handler.Ready(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("cancelled request context must not affect the readiness verdict: expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	// The next poller (with a live context) must also see ready — i.e. the
	// cancelled call must not have poisoned the cache with ready=false.
	req2 := httptest.NewRequest("GET", "/ready", nil)
	req2 = withRequestContext(req2, "REQ-NEXT", DefaultContactMessage)
	rr2 := httptest.NewRecorder()
	handler.Ready(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("cache poisoned by cancelled caller: next poll expected 200, got %d (body: %s)", rr2.Code, rr2.Body.String())
	}
}

// TestReady_Caches asserts the readiness verdict is cached for ~readinessTTL: a
// second call within the TTL does not re-run the probe. We use an injectable
// probe counter and a controllable clock.
func TestReady_Caches(t *testing.T) {
	var probes int32
	clock := time.Now()
	rc := &readinessChecker{
		ttl: readinessTTL,
		now: func() time.Time { return clock },
		probe: func(ctx context.Context) bool {
			atomic.AddInt32(&probes, 1)
			return true
		},
	}

	// First call probes.
	if !rc.isReady() {
		t.Fatal("expected ready")
	}
	// Second call within TTL must NOT probe again.
	if !rc.isReady() {
		t.Fatal("expected ready (cached)")
	}
	if got := atomic.LoadInt32(&probes); got != 1 {
		t.Fatalf("expected exactly 1 probe within TTL, got %d", got)
	}

	// Advance the clock beyond the TTL: the next call re-probes.
	clock = clock.Add(readinessTTL + time.Second)
	if !rc.isReady() {
		t.Fatal("expected ready after TTL")
	}
	if got := atomic.LoadInt32(&probes); got != 2 {
		t.Fatalf("expected a re-probe after TTL, got %d probes", got)
	}
}

// TestProbeStorage covers the directory-readability check directly.
func TestProbeStorage(t *testing.T) {
	if probeStorage("") {
		t.Error("empty path must be not-ready")
	}
	if probeStorage(filepath.Join(t.TempDir(), "nope")) {
		t.Error("missing path must be not-ready")
	}
	// A real, readable, empty directory is ready.
	dir := t.TempDir()
	if !probeStorage(dir) {
		t.Error("readable empty dir must be ready")
	}
	// A file (not a directory) is not-ready.
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if probeStorage(f) {
		t.Error("a file is not a storage mount; must be not-ready")
	}
}
