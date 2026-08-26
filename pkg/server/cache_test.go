package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestCache(t *testing.T, maxEntries int, maxAge time.Duration) *resultCache {
	t.Helper()
	c, err := newResultCache(t.TempDir(), maxEntries, maxAge)
	if err != nil {
		t.Fatalf("newResultCache: %v", err)
	}
	return c
}

func TestResultCache_Disabled(t *testing.T) {
	c, err := newResultCache("", 10, 0)
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
	if c.has("pkg") {
		t.Error("nil has must be false - a disabled cache has nothing to probe for")
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

// TestResultCache_WipesEntriesOnStart pins the single invalidation lever a
// config or binary change relies on: nothing a previous run wrote survives the
// next startup. The wipe is wholesale on the cache-owned entries subdir, so
// the volume root - which is the operator's - must come through untouched.
func TestResultCache_WipesEntriesOnStart(t *testing.T) {
	dir := t.TempDir()
	c1, err := newResultCache(dir, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(c1.dir) != dir || filepath.Base(c1.dir) != entriesDirName {
		t.Fatalf("entries must live in %s/%s, got %s", dir, entriesDirName, c1.dir)
	}
	for _, id := range []string{"pkg-a", "pkg-b"} {
		if err := c1.put(id, "mm", "body"); err != nil {
			t.Fatal(err)
		}
	}
	// Files the operator (or another tool) put in the volume root, including
	// one that looks exactly like an entry filename.
	roots := []string{
		filepath.Join(dir, "README.txt"),
		filepath.Join(dir, "foreign.json"),
		filepath.Join(dir, "Backup.json"),
	}
	for _, path := range roots {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	subdir := filepath.Join(dir, "keep")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}

	c2, err := newResultCache(dir, 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"pkg-a", "pkg-b"} {
		if _, ok := c2.get(id, "mm"); ok {
			t.Errorf("entry %s must not survive a restart", id)
		}
	}
	left, err := os.ReadDir(c2.dir)
	if err != nil {
		t.Fatalf("entries dir must be recreated: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("entries dir must be empty after the wipe, found %d files", len(left))
	}
	for _, path := range append(roots, subdir) {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the wipe must not touch %s: %v", filepath.Base(path), err)
		}
	}

	// The wiped cache is immediately usable again.
	if err := c2.put("pkg-a", "mm", "fresh"); err != nil {
		t.Fatalf("put after wipe: %v", err)
	}
	if body, ok := c2.get("pkg-a", "mm"); !ok || body != "fresh" {
		t.Errorf("round trip after wipe: ok=%v body=%q", ok, body)
	}
}

// TestResultCache_WipeFailureIsFatal pins the hard-fail contract: if the
// entries subdir cannot be cleared, newResultCache reports it instead of
// handing back a cache holding entries of unknown provenance.
func TestResultCache_WipeFailureIsFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	c, err := newResultCache(dir, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.put("pkg-a", "mm", "body"); err != nil {
		t.Fatal(err)
	}

	// r-x on the PARENT: RemoveAll still deletes the entry files (the entries
	// subdir itself stays writable) but cannot rmdir the subdir, and MkdirAll
	// could not recreate it either - so the wipe fails half-done.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	c2, err := newResultCache(dir, 10, 0)
	if err == nil {
		t.Fatal("an unclearable entries dir must fail, not return a cache")
	}
	if c2 != nil {
		t.Error("no cache may be returned when the wipe failed")
	}
	if !strings.Contains(err.Error(), filepath.Join(dir, entriesDirName)) {
		t.Errorf("error must name the directory an operator has to fix, got: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "cannot clear result cache entries dir ") {
		t.Errorf("error must lead with the cause, not a self-prefix, got: %v", err)
	}
}

// TestResultCache_CacheDirIsFile_Fails pins a uid-independent hard fail: a
// resultCacheDir that is a regular file cannot hold the entries subdir, and
// unlike the permission cases root does not get to ignore it.
func TestResultCache_CacheDirIsFile_Fails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := newResultCache(path, 10, 0)
	if err == nil {
		t.Fatal("a cache dir that is a regular file must fail the boot")
	}
	if c != nil {
		t.Error("no cache may be returned when the wipe failed")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the operator's file must survive: %v", statErr)
	}
}

// TestResultCache_ReservedEntriesName pins that the cache only ever deletes a
// real directory under the reserved name: a file or symlink parked there is
// reported to the operator, never removed or followed.
func TestResultCache_ReservedEntriesName(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		dir := t.TempDir()
		entries := filepath.Join(dir, entriesDirName)
		if err := os.WriteFile(entries, []byte("operator data"), 0o600); err != nil {
			t.Fatal(err)
		}

		c, err := newResultCache(dir, 10, 0)
		if err == nil {
			t.Fatal("a non-directory under the reserved name must fail the boot")
		}
		if c != nil {
			t.Error("no cache may be returned when the wipe failed")
		}
		raw, readErr := os.ReadFile(entries)
		if readErr != nil {
			t.Fatalf("the file must survive untouched: %v", readErr)
		}
		if string(raw) != "operator data" {
			t.Errorf("file content changed: %q", raw)
		}
	})

	t.Run("symlink to directory", func(t *testing.T) {
		dir := t.TempDir()
		target := t.TempDir()
		victim := filepath.Join(target, "keep.txt")
		if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries := filepath.Join(dir, entriesDirName)
		if err := os.Symlink(target, entries); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		c, err := newResultCache(dir, 10, 0)
		if err == nil {
			t.Fatal("a symlink under the reserved name must fail the boot")
		}
		if c != nil {
			t.Error("no cache may be returned when the wipe failed")
		}
		if fi, lerr := os.Lstat(entries); lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the symlink must survive unfollowed and unlinked (err=%v)", lerr)
		}
		if _, serr := os.Stat(victim); serr != nil {
			t.Errorf("the symlink target must be untouched: %v", serr)
		}
	})
}

// TestResultCache_CreatesMissingParents pins the fresh-deploy path: a cache dir
// whose parents do not exist yet is created, and the cache works right away.
func TestResultCache_CreatesMissingParents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "var", "lib", "pc", "cache")

	c, err := newResultCache(dir, 10, 0)
	if err != nil {
		t.Fatalf("a missing nested cache dir must be created: %v", err)
	}
	if fi, statErr := os.Stat(filepath.Join(dir, entriesDirName)); statErr != nil || !fi.IsDir() {
		t.Fatalf("entries dir must exist after boot (err=%v)", statErr)
	}
	if err := c.put("pkg-a", "mm", "body"); err != nil {
		t.Fatalf("put on a fresh deploy: %v", err)
	}
	if body, ok := c.get("pkg-a", "mm"); !ok || body != "body" {
		t.Errorf("round trip on a fresh deploy: ok=%v body=%q", ok, body)
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
		// An id that can never be cached must never cost a freshness probe.
		if c.has(id) {
			t.Errorf("has must be false for unsafe/invalid id %q", id)
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

// TestNormalizeModifiedTimestamp pins the property the freshness compare rests
// on: the two spellings Eawag CKAN uses for one instant - package_search's
// millisecond UTC and package_show's microsecond isoformat - canonicalize to
// the same string, while anything unparseable is handed back untouched so the
// compare falls back to byte equality instead of guessing.
func TestNormalizeModifiedTimestamp(t *testing.T) {
	const (
		searchForm = "2025-02-19T12:35:21.757Z"   // Solr: milliseconds, trailing Z
		showForm   = "2025-02-19T12:35:21.757747" // isoformat: microseconds, no zone
	)
	// Solr truncates the sub-second part (757747 -> 757); rounding would drift
	// the two endpoints apart by a millisecond again.
	if got := normalizeModifiedTimestamp(showForm); got != searchForm {
		t.Errorf("normalizeModifiedTimestamp(%q) = %q, want %q", showForm, got, searchForm)
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"search form", searchForm, searchForm},
		{"no fraction", "2025-02-19T12:35:21", "2025-02-19T12:35:21.000Z"},
		{"offset zone", "2025-02-19T13:35:21.757+01:00", searchForm},
		{"garbage", "not-a-time", "not-a-time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeModifiedTimestamp(tc.in); got != tc.want {
				t.Errorf("normalizeModifiedTimestamp(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestResultCache_TimestampSpellingsHit pins the cache-level consequence in both
// directions: an entry stored with the timestamp package_show returned is found
// again with the one package_search reports for the same instant, and one stored
// with the probe's spelling is found with package_show's - put-side and get-side
// normalization each carry a case of their own. An instant a millisecond away,
// the finest resolution the probe reports, still misses.
func TestResultCache_TimestampSpellingsHit(t *testing.T) {
	c := newTestCache(t, 10, 0)
	if err := c.put("my-pkg", "2025-02-19T12:35:21.757747", `{"result":"x"}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	body, ok := c.get("my-pkg", "2025-02-19T12:35:21.757Z")
	if !ok || body != `{"result":"x"}` {
		t.Fatalf("the probe's spelling must hit the stored entry, got ok=%v body=%q", ok, body)
	}
	if _, ok := c.get("my-pkg", "2025-02-19T12:35:21.758Z"); ok {
		t.Error("a different instant must still miss")
	}

	// The reverse: what was stored is already canonical, so only the get side can
	// still bridge to the document's own spelling.
	if err := c.put("rev-pkg", "2025-02-19T12:35:21.757Z", `{"result":"y"}`); err != nil {
		t.Fatalf("put: %v", err)
	}
	body, ok = c.get("rev-pkg", "2025-02-19T12:35:21.757747")
	if !ok || body != `{"result":"y"}` {
		t.Fatalf("the document's spelling must hit the entry stored from the probe's, got ok=%v body=%q", ok, body)
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
