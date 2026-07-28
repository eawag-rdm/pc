package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestCache(t *testing.T, maxEntries int, maxAge time.Duration) *resultCache {
	t.Helper()
	c, err := newResultCache(t.TempDir(), "fp-1", maxEntries, maxAge)
	if err != nil {
		t.Fatalf("newResultCache: %v", err)
	}
	return c
}

func TestConfigFingerprint(t *testing.T) {
	a := configFingerprint([]byte("config-a"))
	b := configFingerprint([]byte("config-b"))
	if a == b {
		t.Error("different config bytes must yield different fingerprints")
	}
	if a != configFingerprint([]byte("config-a")) {
		t.Error("fingerprint must be deterministic")
	}
	if len(a) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(a))
	}
}

func TestResultCache_Disabled(t *testing.T) {
	c, err := newResultCache("", "fp", 10, 0)
	if err != nil {
		t.Fatalf("empty dir must not error: %v", err)
	}
	if c != nil {
		t.Fatal("empty dir must disable the cache (nil)")
	}
	// nil methods are no-ops.
	if err := c.put("pkg", "2026-01-01", "body"); err != nil {
		t.Errorf("nil put must be a no-op, got %v", err)
	}
	if _, ok := c.get("pkg", "2026-01-01"); ok {
		t.Error("nil get must miss")
	}
}

func TestResultCache_RoundTrip(t *testing.T) {
	c := newTestCache(t, 10, 0)

	if _, ok := c.get("my-pkg", "2026-01-01T10:00:00"); ok {
		t.Fatal("expected miss before put")
	}
	if err := c.put("my-pkg", "2026-01-01T10:00:00", `{"result":"x"}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	body, ok := c.get("my-pkg", "2026-01-01T10:00:00")
	if !ok || body != `{"result":"x"}` {
		t.Fatalf("expected hit with stored body, got ok=%v body=%q", ok, body)
	}

	// A changed metadata_modified is a miss: the package changed upstream.
	if _, ok := c.get("my-pkg", "2026-02-02T00:00:00"); ok {
		t.Error("expected miss for a different metadata_modified")
	}
}

func TestResultCache_FingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	c1, err := newResultCache(dir, "fp-old", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c1.put("pkg", "mm", "body"); err != nil {
		t.Fatal(err)
	}

	// Same dir, new fingerprint (config or version changed): entry invalid.
	c2, err := newResultCache(dir, "fp-new", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.get("pkg", "mm"); ok {
		t.Error("expected miss after fingerprint change")
	}
}

func TestResultCache_TTLExpiry(t *testing.T) {
	c := newTestCache(t, 10, time.Hour)
	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return base }

	if err := c.put("pkg", "mm", "body"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.get("pkg", "mm"); !ok {
		t.Fatal("expected hit within TTL")
	}
	c.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, ok := c.get("pkg", "mm"); ok {
		t.Error("expected miss after TTL expiry")
	}
}

func TestResultCache_Eviction(t *testing.T) {
	c := newTestCache(t, 2, 0)
	for i, id := range []string{"pkg-a", "pkg-b", "pkg-c"} {
		if err := c.put(id, "mm", "body"); err != nil {
			t.Fatal(err)
		}
		// Backdate mtimes so eviction order is deterministic despite
		// sub-second put spacing.
		mod := time.Now().Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(filepath.Join(c.dir, id+".json"), mod, mod); err != nil {
			t.Fatal(err)
		}
		c.prune()
	}

	if _, ok := c.get("pkg-a", "mm"); ok {
		t.Error("oldest entry should have been evicted")
	}
	for _, id := range []string{"pkg-b", "pkg-c"} {
		if _, ok := c.get(id, "mm"); !ok {
			t.Errorf("entry %s should have survived eviction", id)
		}
	}
}

func TestResultCache_RejectsUnsafeIDs(t *testing.T) {
	c := newTestCache(t, 10, 0)
	for _, id := range []string{"../escape", "a/b", "UPPER", "x", ".", ""} {
		if err := c.put(id, "mm", "body"); err == nil {
			t.Errorf("put must reject unsafe/invalid id %q", id)
		}
		if _, ok := c.get(id, "mm"); ok {
			t.Errorf("get must miss for unsafe/invalid id %q", id)
		}
	}
	// Nothing may have been written outside (or inside) the cache dir.
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty cache dir, found %d entries", len(entries))
	}
}

func TestResultCache_EmptyMetadataModified(t *testing.T) {
	c := newTestCache(t, 10, 0)
	// Without the freshness signal nothing may be stored or served.
	if err := c.put("pkg", "", "body"); err != nil {
		t.Fatalf("empty metadata_modified put must be a silent no-op, got %v", err)
	}
	if _, ok := c.get("pkg", ""); ok {
		t.Error("empty metadata_modified must never hit")
	}
	entries, _ := os.ReadDir(c.dir)
	if len(entries) != 0 {
		t.Error("empty metadata_modified must not create an entry")
	}
}

func TestResultCache_CorruptEntryIsMiss(t *testing.T) {
	c := newTestCache(t, 10, 0)
	if err := os.WriteFile(filepath.Join(c.dir, "pkg.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.get("pkg", "mm"); ok {
		t.Error("corrupt entry must be a miss, not an error or a hit")
	}
}
