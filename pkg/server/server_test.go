package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestServerConfig writes a minimal valid PC config to a temp file and
// returns a server.Config pointing at it, with the listen address overridden to
// addr.
func newTestServerConfig(t *testing.T, addr, ckanURL, storagePath string) Config {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/pc.toml"
	contents := "" +
		"[collector.CkanCollector]\n" +
		"attrs = {url = \"" + ckanURL + "\", token = \"\", verify = false, ckan_storage_path = \"" + storagePath + "\"}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Config{Address: addr, ConfigPath: path}
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

	// Give Serve a moment to enter its accept loop, then shut down.
	time.Sleep(50 * time.Millisecond)
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
