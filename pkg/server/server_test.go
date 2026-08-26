package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testChecksTOML is a small, well-typed check configuration on the [[rule]]
// surface; boot validation is checks.Compile, which binds every rule's
// parameters at server.New.
const testChecksTOML = "" +
	"[[rule]]\n" +
	"name  = \"IsFreeOfKeywords\"\n" +
	"check = \"IsFreeOfKeywords\"\n" +
	"params = [{keywords = [\"password\"], info = \"Sensitive keyword found:\"}]\n" +
	"[[rule]]\n" +
	"name  = \"IsValidName\"\n" +
	"check = \"IsValidName\"\n" +
	"params = [{disallowed_names = [\".DS_Store\"]}]\n" +
	"[[rule]]\n" +
	"name  = \"HasReadme\"\n" +
	"check = \"HasReadme\"\n" +
	"params = [{readme_names = [\"readme.md\", \"readme.txt\"]}]\n"

// newTestServerConfig writes a minimal valid PC config to a temp file and
// returns a server.Config pointing at it, with the listen address overridden to
// addr.
func newTestServerConfig(t *testing.T, addr, ckanURL, storagePath string) Config {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/pc.toml"
	contents := testChecksTOML +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"" + ckanURL + "\", token = \"\", verify = false, ckan_storage_path = \"" + storagePath + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Config{Address: addr, ConfigPath: path}
}

// waitListening blocks until addr accepts a TCP connection, giving tests a
// deterministic "the server is serving" signal instead of a fixed sleep that is
// CI-flaky (too short races the accept loop, too long wastes wall-clock).
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening on %s within deadline: %v", addr, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestNew_RejectsInvalidFilterPattern asserts the boot gate for the rules' file
// filters: an include/exclude pattern that does not compile refuses the startup
// (the operator's config is wrong), instead of failing - and alerting on -
// every /analyze request while /ready reports the server usable.
func TestNew_RejectsInvalidFilterPattern(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pc.toml"
	contents := testChecksTOML +
		"[[rule]]\n" +
		"name    = \"HasOnlyASCII\"\n" +
		"check   = \"HasOnlyASCII\"\n" +
		"include = [\"(\"]\n" +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"http://127.0.0.1:1\", token = \"\", verify = false, ckan_storage_path = \"" + dir + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	srv, err := New(Config{Address: "127.0.0.1:0", ConfigPath: path})
	if err == nil {
		t.Fatal("New accepted a config whose filter pattern does not compile")
	}
	if srv != nil {
		t.Error("New must not return a server when the config is invalid")
	}
	if !strings.Contains(err.Error(), "HasOnlyASCII") {
		t.Errorf("error must name the offending rule: %v", err)
	}
}

// TestNew_ScrubsConfigToken asserts the server blanks the CLI-only TOML
// collector token at boot, so no server code path can ever authenticate an
// upstream CKAN call with the operator's token (per-request Bearer tokens or
// anonymous access are the only auth paths).
func TestNew_ScrubsConfigToken(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pc.toml"
	contents := testChecksTOML +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"http://127.0.0.1:1\", token = \"toml-secret\", verify = false, ckan_storage_path = \"" + dir + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	srv, err := New(Config{Address: "127.0.0.1:0", ConfigPath: path})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	got, ok := srv.pcConfig.Collectors["CkanCollector"].Attrs["token"].(string)
	if !ok {
		t.Fatal("token attr is no longer a string after boot")
	}
	if got != "" {
		t.Errorf("config token survived boot as %q; the server must blank it", got)
	}
}

// TestNew_MissingCkanAttr_FailsAtBoot asserts that an incomplete
// [collector.CkanCollector] section makes server.New fail at startup with a clear
// error, rather than booting and surfacing the problem as a per-request
// internal_error 500 (spec §5 fail-fast).
func TestNew_MissingCkanAttr_FailsAtBoot(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pc.toml"
	// A CkanCollector with url + token + verify but NO ckan_storage_path:
	// previously this booted and only failed when /analyze called the collector.
	contents := "" +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"http://127.0.0.1:1\", token = \"\", verify = false}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := New(Config{Address: "127.0.0.1:0", ConfigPath: path})
	if err == nil {
		t.Fatal("expected New to fail at boot for missing CkanCollector attrs")
	}
	if !strings.Contains(err.Error(), "ckan_storage_path") {
		t.Errorf("startup error should name the missing attr, got: %v", err)
	}
}

// TestNew_BadCheckParams_FailsAtBoot asserts that a wrong-typed check
// parameter makes server.New fail at startup with a clear error - instead of
// booting and misbehaving on the first /analyze.
func TestNew_BadCheckParams_FailsAtBoot(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pc.toml"
	// Valid CkanCollector, one [[rule]] with a wrong-typed parameter.
	contents := "" +
		"[[rule]]\n" +
		"name  = \"credentials\"\n" +
		"check = \"IsFreeOfKeywords\"\n" +
		"  [rule.params]\n" +
		"  keywords = \"password\"\n" +
		"  info     = \"found\"\n" +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"http://127.0.0.1:1\", token = \"\", verify = false, ckan_storage_path = \"" + t.TempDir() + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := New(Config{Address: "127.0.0.1:0", ConfigPath: path})
	if err == nil {
		t.Fatal("expected New to fail at boot for a wrong-typed rule parameter")
	}
	if !strings.Contains(err.Error(), "keywords") {
		t.Errorf("startup error should name the bad parameter, got: %v", err)
	}
}

// newTestServerConfigWithResultCache builds a server Config whose pc.toml sets
// [server] resultCacheDir, for the boot-time cache tests.
func newTestServerConfigWithResultCache(t *testing.T, cacheDir string) Config {
	t.Helper()
	path := t.TempDir() + "/pc.toml"
	contents := "" +
		"[server]\n" +
		"resultCacheDir = \"" + cacheDir + "\"\n" +
		testChecksTOML +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"http://127.0.0.1:1\", token = \"\", verify = false, ckan_storage_path = \"" + t.TempDir() + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Config{Address: "127.0.0.1:0", ConfigPath: path}
}

// TestNew_ResultCacheEnabled_ClearsEntriesAtBoot asserts the success path of the
// same contract: with a healthy cache dir, New wires the cache into the handler
// and the (cleared) entries subdir exists before the first request.
func TestNew_ResultCacheEnabled_ClearsEntriesAtBoot(t *testing.T) {
	cacheDir := t.TempDir()
	srv, err := New(newTestServerConfigWithResultCache(t, cacheDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.handler.cache == nil {
		t.Fatal("a configured resultCacheDir must leave the handler with a cache")
	}
	entriesDir := filepath.Join(cacheDir, entriesDirName)
	if fi, statErr := os.Stat(entriesDir); statErr != nil || !fi.IsDir() {
		t.Fatalf("%s must exist after boot (err=%v)", entriesDir, statErr)
	}
	if srv.handler.cache.dir != entriesDir {
		t.Errorf("cache dir = %q, want %q", srv.handler.cache.dir, entriesDir)
	}
}

// TestNew_ResultCacheWipeFailure_FailsAtBoot asserts the cache's hard-fail
// contract reaches the operator: when the entries subdir cannot be cleared,
// New refuses to build the server (cmd/pc-server turns that into a log.Fatalf
// on stderr) instead of booting on entries of unknown provenance.
func TestNew_ResultCacheWipeFailure_FailsAtBoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cacheDir := t.TempDir()
	cfg := newTestServerConfigWithResultCache(t, cacheDir)
	// r-x: the entries subdir cannot be created (nor removed once it exists).
	if err := os.Chmod(cacheDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cacheDir, 0o700) })

	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected New to fail at boot when the result cache cannot be cleared")
	}
	if !strings.Contains(err.Error(), cacheDir) {
		t.Errorf("startup error should name the cache directory, got: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "result cache startup: ") {
		t.Errorf("startup error should carry New's category prefix, got: %v", err)
	}
}

// TestNew_BadAllowedClientsCIDR_FailsAtBoot asserts the allow-list's CIDR-only
// grammar reaches the operator as a startup failure naming the entry, instead of
// the entry being dropped - a dropped entry silently changes who can reach
// /analyze.
func TestNew_BadAllowedClientsCIDR_FailsAtBoot(t *testing.T) {
	srv, err := New(newTestServerConfigWithServerLine(t, `allowedClients = ["192.0.2.0/24", "10.0.0.1"]`))
	if err == nil {
		t.Fatal("expected New to fail at boot for a non-CIDR allowedClients entry")
	}
	if srv != nil {
		t.Error("New must not return a server when the config is invalid")
	}
	if !strings.Contains(err.Error(), "10.0.0.1") {
		t.Errorf("startup error should name the offending entry, got: %v", err)
	}
}

// TestNew_BadTrustedProxiesCIDR_FailsAtBoot asserts trustedProxies runs the same
// strict boot parse as allowedClients: an entry with host bits set stops the
// start naming both readings. It used to be accepted and masked to 192.0.2.0/24,
// silently widening proxy trust to the whole /24.
func TestNew_BadTrustedProxiesCIDR_FailsAtBoot(t *testing.T) {
	srv, err := New(newTestServerConfigWithServerLine(t, `trustedProxies = ["127.0.0.1/32", "192.0.2.7/24"]`))
	if err == nil {
		t.Fatal("expected New to fail at boot for a trustedProxies entry with host bits")
	}
	if srv != nil {
		t.Error("New must not return a server when the config is invalid")
	}
	const want = `trustedProxies entry "192.0.2.7/24" has host bits set: write "192.0.2.0/24" for the network, or "192.0.2.7/32" for the single host`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("startup error should name the key and both readings of the entry, got: %v", err)
	}
}

// TestNew_TrustedProxies_ReachTheLimiter asserts the parsed prefixes are wired
// into the limiter New builds. Without that wiring trustProxyHeaders is inert -
// X-Real-IP is ignored and every client behind the proxy shares the proxy's
// rate-limit bucket - and the boot succeeds silently either way.
func TestNew_TrustedProxies_ReachTheLimiter(t *testing.T) {
	srv, err := New(newTestServerConfigWithServerLine(t, "trustProxyHeaders = true\ntrustedProxies = [\"127.0.0.1/32\"]"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/analyze", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Real-IP", "203.0.113.7")
	if got := srv.handler.limiter.clientIPKey(req); got != "203.0.113.7" {
		t.Errorf("key from the trusted proxy = %q, want the X-Real-IP client 203.0.113.7", got)
	}
}

// TestNew_ClientAllowlist_GatesAnalyzeAheadOfTheLimiter drives the fully wired
// chain: an unlisted client gets 403 client_not_allowed and, because the gate is
// the OUTERMOST analyze middleware, never reaches the rate limiter - so a flood
// of denied requests can neither fill the limiter map nor evict honest clients'
// counters. /health and /ready are not wrapped by the gate.
func TestNew_ClientAllowlist_GatesAnalyzeAheadOfTheLimiter(t *testing.T) {
	srv, err := New(newTestServerConfigWithServerLine(t, `allowedClients = ["192.0.2.0/24"]`))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const unlisted = "198.51.100.9:1111"
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{"package_id":"some-package"}`))
		req.RemoteAddr = unlisted
		rr := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rr, req)
		return rr
	}

	rr := do("POST", "/api/v1/analyze")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unlisted client: status %d, want 403", rr.Code)
	}
	if code := decodeEnvelope(t, rr).Error.Code; code != CodeClientNotAllowed {
		t.Errorf("unlisted client: code %q, want %q", code, CodeClientNotAllowed)
	}
	if got := srv.handler.limiter.trackedKeys(); got != 0 {
		t.Errorf("denied request created %d limiter entries; the gate must run before RateLimitPerIP", got)
	}

	if rr := do("GET", "/health"); rr.Code != http.StatusOK {
		t.Errorf("/health from an unlisted client: status %d, want 200", rr.Code)
	}
}

// TestNew_NoAllowedClients_GateNotInstalled asserts the feature is off by
// default: with the key absent no gate is installed at all, so an arbitrary
// client still reaches the analyze chain exactly as before.
func TestNew_NoAllowedClients_GateNotInstalled(t *testing.T) {
	srv, err := New(newTestServerConfig(t, "127.0.0.1:0", "http://127.0.0.1:1", t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(srv.handler.allowedClients) != 0 {
		t.Fatalf("allowedClients = %v, want none when the key is absent", srv.handler.allowedClients)
	}

	req := httptest.NewRequest("POST", "/api/v1/analyze", strings.NewReader(`{}`))
	req.RemoteAddr = "198.51.100.9:1111"
	rr := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rr, req)

	// A wrongly-installed gate answers 403 client_not_allowed; the 404 check
	// keeps the route itself pinned as registered.
	if rr.Code == http.StatusForbidden {
		t.Fatalf("an arbitrary client was refused with %d; without allowedClients no gate may be installed", rr.Code)
	}
	if code := decodeEnvelope(t, rr).Error.Code; code == CodeClientNotAllowed {
		t.Fatalf("response carries %q; without allowedClients no gate may be installed", code)
	}
	if rr.Code == http.StatusNotFound {
		t.Fatal("the analyze route is not registered")
	}
}

// newTestServerConfigWithServerLine builds a server Config whose pc.toml carries
// the given extra line in its [server] section, for tests that pin how a single
// setting is wired at boot.
func newTestServerConfigWithServerLine(t *testing.T, serverLine string) Config {
	t.Helper()
	path := t.TempDir() + "/pc.toml"
	contents := "" +
		"[server]\n" +
		serverLine + "\n" +
		testChecksTOML +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"http://127.0.0.1:1\", token = \"\", verify = false, ckan_storage_path = \"" + t.TempDir() + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Config{Address: "127.0.0.1:0", ConfigPath: path}
}

// TestNew_WriteTimeout_ExceedsRequestTimeout asserts that the constructed
// http.Server's WriteTimeout is derived from the configured
// requestTimeoutSeconds and is strictly LONGER than it (by writeTimeoutMargin).
// This guards the D3 invariant: if the socket WriteTimeout fired at or before
// the analysis hard timeout, the handler could not finish writing its clean
// analysis_timeout (504) envelope and the client would see a dropped connection.
func TestNew_WriteTimeout_ExceedsRequestTimeout(t *testing.T) {
	const requestTimeoutSeconds = 600 // raised above the old hardcoded 300s
	srv, err := New(newTestServerConfigWithServerLine(t, fmt.Sprintf("requestTimeoutSeconds = %d", requestTimeoutSeconds)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	requestTimeout := time.Duration(requestTimeoutSeconds) * time.Second
	got := srv.httpServer.WriteTimeout
	if got <= requestTimeout {
		t.Fatalf("WriteTimeout (%s) must exceed the analysis request timeout (%s) so the 504 envelope can flush", got, requestTimeout)
	}
	if want := requestTimeout + writeTimeoutMargin; got != want {
		t.Errorf("WriteTimeout = %s, want requestTimeout+margin = %s", got, want)
	}
}

// TestNew_WriteTimeout_DefaultRequestTimeout asserts that with no [server]
// requestTimeoutSeconds set, the WriteTimeout falls back to the default request
// timeout plus the margin (so the invariant holds for the default too).
func TestNew_WriteTimeout_DefaultRequestTimeout(t *testing.T) {
	cfg := newTestServerConfig(t, "127.0.0.1:0", "http://127.0.0.1:1", t.TempDir())
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := defaultRequestTimeout + writeTimeoutMargin; srv.httpServer.WriteTimeout != want {
		t.Errorf("WriteTimeout = %s, want default+margin = %s", srv.httpServer.WriteTimeout, want)
	}
}

// TestServer_DrainTimeout asserts that the shutdown drain bound tracks the
// configured request timeout (plus writeTimeoutMargin) instead of a hardcoded
// constant: with the default it is 330s, and raising requestTimeoutSeconds
// raises the drain with it. A fixed drain would cut a long analysis short.
func TestServer_DrainTimeout(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg := newTestServerConfig(t, "127.0.0.1:0", "http://127.0.0.1:1", t.TempDir())
		srv, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if got, want := srv.DrainTimeout(), 330*time.Second; got != want {
			t.Errorf("DrainTimeout = %s, want %s", got, want)
		}
	})

	t.Run("custom request timeout", func(t *testing.T) {
		srv, err := New(newTestServerConfigWithServerLine(t, "requestTimeoutSeconds = 600"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// 630s pinned literally: re-deriving from writeTimeoutMargin would let
		// an edited margin pass unnoticed.
		if got, want := srv.DrainTimeout(), 630*time.Second; got != want {
			t.Errorf("DrainTimeout = %s, want requestTimeout+margin = %s", got, want)
		}
	})
}

// TestServer_BindFailure_ReturnsError asserts that ListenAndServe returns a
// non-ErrServerClosed error when the listen address cannot be bound (e.g. the
// port is already in use), so main can exit non-zero instead of hanging on the
// shutdown channel (the §10 bind-failure bug / §9 hardening).
func TestServer_BindFailure_ReturnsError(t *testing.T) {
	// Occupy a port so the server's bind fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	cfg := newTestServerConfig(t, addr, "http://127.0.0.1:1", t.TempDir())
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a bind error, got nil (would hang main)")
		}
		if errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("bind failure must NOT be ErrServerClosed, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe neither bound nor errored: bind-failure hang regression")
	}
}

// TestServer_CleanShutdown_ReturnsErrServerClosed asserts a graceful Shutdown
// causes ListenAndServe to return http.ErrServerClosed (the value main treats as
// the non-fatal, expected path).
func TestServer_CleanShutdown_ReturnsErrServerClosed(t *testing.T) {
	cfg := newTestServerConfig(t, "127.0.0.1:0", "http://127.0.0.1:1", t.TempDir())
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Bind explicitly so we can serve on an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.httpServer.Serve(ln) }()

	// Wait deterministically until Serve is accepting connections, then shut
	// down - polling the bound address beats a fixed sleep (CI-flaky).
	waitListening(t, ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("expected ErrServerClosed after Shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}

// TestServer_GracefulShutdown_DrainsAndRejects drives the full server: it starts
// a request that blocks inside the handler, begins draining, and asserts (a) the
// in-flight request completes successfully (drained, not dropped) and (b) a new
// request during the drain is rejected with server_restarting (503).
func TestServer_GracefulShutdown_DrainsAndRejects(t *testing.T) {
	// A blocking analyze handler: it signals it has started, then waits for a
	// release. We exercise the Draining middleware + the in-flight drain directly
	// against a real listener.
	cfg := newTestServerConfig(t, "127.0.0.1:0", "http://127.0.0.1:1", t.TempDir())
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	// Replace the server handler with a tiny chain that wraps a blocking handler
	// with Draining + RequestContext so the in-flight semantics are real.
	blocking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("done"))
	})
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/analyze", srv.handler.Draining(blocking))
	srv.httpServer.Handler = srv.handler.RequestContext(srv.handler.CORS(mux))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.httpServer.Serve(ln)
	baseURL := "http://" + ln.Addr().String()

	// Fire the in-flight request.
	var wg sync.WaitGroup
	wg.Add(1)
	var inflightStatus int
	go func() {
		defer wg.Done()
		resp, err := http.Post(baseURL+"/api/v1/analyze", "application/json", strings.NewReader(`{"package_id":"x"}`))
		if err != nil {
			t.Errorf("in-flight request error: %v", err)
			return
		}
		defer resp.Body.Close()
		io.ReadAll(resp.Body)
		inflightStatus = resp.StatusCode
	}()

	<-started // the in-flight request is now inside the handler

	// Begin draining (as Shutdown does). The in-flight handler still runs.
	srv.handler.BeginDraining()

	// A NEW request during the drain must be rejected with server_restarting.
	newResp, err := http.Post(baseURL+"/api/v1/analyze", "application/json", strings.NewReader(`{"package_id":"y"}`))
	if err != nil {
		t.Fatalf("new request error: %v", err)
	}
	body, _ := io.ReadAll(newResp.Body)
	newResp.Body.Close()
	if newResp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("new request during drain: expected 503, got %d", newResp.StatusCode)
	}
	if !strings.Contains(string(body), CodeServerRestarting) {
		t.Errorf("new request during drain: expected %q in body, got %s", CodeServerRestarting, string(body))
	}

	// Release the in-flight handler; it must finish successfully (drained).
	close(release)
	wg.Wait()
	if inflightStatus != http.StatusOK {
		t.Errorf("in-flight request must drain to completion (200), got %d", inflightStatus)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.httpServer.Shutdown(ctx)
}
