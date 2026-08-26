package server

import (
	"errors"
	"fmt"
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

// What it takes to be a storable, servable entry: the filename is built from the
// parsed metadata_modified, and the body is served unparsed, so it has to be the
// JSON object a formatter produces.
const (
	modifiedFixture = "2026-01-01T10:00:00"
	bodyFixture     = `{"result":"x"}`
)

func TestResultCache_Disabled(t *testing.T) {
	c, err := newResultCache("", 10, 0)
	if err != nil {
		t.Fatalf("empty dir must not error: %v", err)
	}
	if c != nil {
		t.Fatal("empty dir must disable the cache (nil)")
	}
	// nil methods are no-ops.
	if err := c.put("pkg", "2026-01-01", []byte("body")); err != nil {
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
	const body = `{"result":"x"}`

	if _, ok := c.get("my-pkg", "2026-01-01T10:00:00"); ok {
		t.Fatal("expected miss before put")
	}
	if err := c.put("my-pkg", "2026-01-01T10:00:00", []byte(body)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok := c.get("my-pkg", "2026-01-01T10:00:00")
	if !ok || string(got) != body {
		t.Fatalf("expected hit with stored body, got ok=%v body=%q", ok, got)
	}

	// The on-disk contract, and the reason a hit costs one read: the canonical
	// timestamp is in the NAME, and the file holds the body and nothing else -
	// no wrapper to parse, no escaping to undo.
	raw, err := os.ReadFile(filepath.Join(c.dir, "my-pkg.20260101T100000000Z.json"))
	if err != nil {
		t.Fatalf("entry must be stored under its timestamp token: %v", err)
	}
	if string(raw) != body {
		t.Errorf("entry file holds %q, want the body verbatim (%q)", raw, body)
	}

	// A changed metadata_modified is a miss: the package changed upstream. It
	// names a file that does not exist, so the name alone decides it and the
	// stored entry is never opened.
	if _, ok := c.get("my-pkg", "2026-02-02T00:00:00"); ok {
		t.Error("expected miss for a different metadata_modified")
	}
}

// TestResultCache_StaleReplacement pins that a re-analysed package leaves ONE
// entry behind, and only its own. The timestamp is part of the filename, so a
// new entry no longer overwrites its predecessor - put has to remove it, or
// every version of every package would stay on disk and be served for its own
// timestamp forever. "pkg-a" is cached alongside "pkg": the separator in the
// prefix is what keeps re-analysing one package from deleting the other's entry.
func TestResultCache_StaleReplacement(t *testing.T) {
	c := newTestCache(t, 10, 0)
	const (
		before  = "2026-01-01T10:00:00"
		after   = "2026-01-02T11:00:00"
		newest  = `{"result":"new"}`
		sibling = `{"result":"sibling"}`
	)
	if err := c.put("pkg-a", before, []byte(sibling)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.put("pkg", before, []byte(`{"result":"old"}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.put("pkg", after, []byte(newest)); err != nil {
		t.Fatalf("put: %v", err)
	}

	left, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		names := make([]string, 0, len(left))
		for _, de := range left {
			names = append(names, de.Name())
		}
		t.Errorf("each package must be left with one entry, found %d: %v", len(left), names)
	}
	if _, ok := c.get("pkg", before); ok {
		t.Error("the superseded entry must not be servable")
	}
	body, ok := c.get("pkg", after)
	if !ok || string(body) != newest {
		t.Errorf("the new entry must be served byte for byte, got ok=%v body=%q", ok, body)
	}
	body, ok = c.get("pkg-a", before)
	if !ok || string(body) != sibling {
		t.Errorf("a package whose id starts with another's must survive its re-analysis, got ok=%v body=%q", ok, body)
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
		if err := c1.put(id, modifiedFixture, []byte(bodyFixture)); err != nil {
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
		if _, ok := c2.get(id, modifiedFixture); ok {
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
	const fresh = `{"result":"fresh"}`
	if err := c2.put("pkg-a", modifiedFixture, []byte(fresh)); err != nil {
		t.Fatalf("put after wipe: %v", err)
	}
	if body, ok := c2.get("pkg-a", modifiedFixture); !ok || string(body) != fresh {
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
	if err := c.put("pkg-a", modifiedFixture, []byte(bodyFixture)); err != nil {
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
	if err := c.put("pkg-a", modifiedFixture, []byte(bodyFixture)); err != nil {
		t.Fatalf("put on a fresh deploy: %v", err)
	}
	if body, ok := c.get("pkg-a", modifiedFixture); !ok || string(body) != bodyFixture {
		t.Errorf("round trip on a fresh deploy: ok=%v body=%q", ok, body)
	}
}

// TestResultCache_TTLExpiry walks the entry's whole TTL life. The mtime is the
// clock's other hand, so both are set here: the file is aged with Chtimes and
// now() reads from the same instant, which makes the boundary exact - an entry
// aged exactly maxAge is not expired yet.
func TestResultCache_TTLExpiry(t *testing.T) {
	c := newTestCache(t, 10, time.Hour)

	if err := c.put("pkg", modifiedFixture, []byte(bodyFixture)); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.get("pkg", modifiedFixture); !ok {
		t.Fatal("expected hit within TTL")
	}

	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(c.entryPath("pkg", modifiedFixture), base, base); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return base.Add(time.Hour) }
	if _, ok := c.get("pkg", modifiedFixture); !ok {
		t.Error("an entry aged exactly maxAge is not past it and must still be served")
	}
	c.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, ok := c.get("pkg", modifiedFixture); ok {
		t.Error("expected miss after TTL expiry")
	}
}

func TestResultCache_Eviction(t *testing.T) {
	c := newTestCache(t, 2, 0)
	for i, id := range []string{"pkg-a", "pkg-b", "pkg-c"} {
		if err := c.put(id, modifiedFixture, []byte(bodyFixture)); err != nil {
			t.Fatal(err)
		}
		// Backdate mtimes so eviction order is deterministic despite
		// sub-second put spacing.
		mod := time.Now().Add(time.Duration(i-10) * time.Minute)
		if err := os.Chtimes(c.entryPath(id, modifiedFixture), mod, mod); err != nil {
			t.Fatal(err)
		}
		c.prune()
	}

	if _, ok := c.get("pkg-a", modifiedFixture); ok {
		t.Error("oldest entry should have been evicted")
	}
	for _, id := range []string{"pkg-b", "pkg-c"} {
		if _, ok := c.get(id, modifiedFixture); !ok {
			t.Errorf("entry %s should have survived eviction", id)
		}
	}
}

func TestResultCache_RejectsUnsafeIDs(t *testing.T) {
	c := newTestCache(t, 10, 0)
	// One valid entry first: has() answers from the entries listing, so against an
	// empty dir it would report false for any id at all.
	if err := c.put("pkg-a", modifiedFixture, []byte(bodyFixture)); err != nil {
		t.Fatalf("seeding a valid entry: %v", err)
	}
	for _, id := range []string{"../escape", "a/b", "UPPER", "x", ".", ""} {
		if err := c.put(id, modifiedFixture, []byte(bodyFixture)); err == nil {
			t.Errorf("put must reject unsafe/invalid id %q", id)
		}
		if _, ok := c.get(id, modifiedFixture); ok {
			t.Errorf("get must miss for unsafe/invalid id %q", id)
		}
		// An id that can never be cached must never cost a freshness probe.
		if c.has(id) {
			t.Errorf("has must be false for unsafe/invalid id %q", id)
		}
	}
	// Nothing may have been written outside (or inside) the cache dir - the
	// seeded entry is all that may be there.
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, de := range entries {
		names = append(names, de.Name())
	}
	if len(names) != 1 || names[0] != filepath.Base(c.entryPath("pkg-a", modifiedFixture)) {
		t.Errorf("only the seeded entry may exist, found %v", names)
	}
}

func TestResultCache_EmptyMetadataModified(t *testing.T) {
	c := newTestCache(t, 10, 0)
	// Without the freshness signal nothing may be stored or served, and put says
	// so rather than reporting a write that never happened as a success.
	if err := c.put("pkg", "", []byte(bodyFixture)); !errors.Is(err, errUncacheableTimestamp) {
		t.Fatalf("empty metadata_modified put must report errUncacheableTimestamp, got %v", err)
	}
	if _, ok := c.get("pkg", ""); ok {
		t.Error("empty metadata_modified must never hit")
	}
	entries, _ := os.ReadDir(c.dir)
	if len(entries) != 0 {
		t.Error("empty metadata_modified must not create an entry")
	}
}

// TestResultCache_UnparseableMetadataModified pins that a timestamp neither CKAN
// spelling parses is treated exactly like a missing one: it names no entry file,
// so nothing is written and every request for that package is a miss - reported
// as errUncacheableTimestamp, since that state lasts as long as CKAN reports the
// timestamp and no other signal exists for it. Caching it under the raw input
// instead would put unvalidated, unbounded caller data in a filename and make the
// token collide with whatever else strips to the same bytes.
func TestResultCache_UnparseableMetadataModified(t *testing.T) {
	c := newTestCache(t, 10, 0)
	const garbage = "yesterday afternoon"

	if err := c.put("pkg", garbage, []byte(bodyFixture)); !errors.Is(err, errUncacheableTimestamp) {
		t.Fatalf("an unparseable metadata_modified put must report errUncacheableTimestamp, got %v", err)
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, de := range entries {
			names = append(names, de.Name())
		}
		t.Errorf("an unparseable metadata_modified must not create an entry, found %v", names)
	}
	if _, ok := c.get("pkg", garbage); ok {
		t.Error("an unparseable metadata_modified must never hit")
	}
}

// TestNormalizeModifiedTimestamp pins the property the entry name rests on: the
// two spellings Eawag CKAN uses for one instant - package_search's millisecond
// UTC and package_show's microsecond isoformat - canonicalize to the same
// string, while anything unparseable is refused rather than guessed at.
func TestNormalizeModifiedTimestamp(t *testing.T) {
	const (
		searchForm = "2025-02-19T12:35:21.757Z"   // Solr: milliseconds, trailing Z
		showForm   = "2025-02-19T12:35:21.757747" // isoformat: microseconds, no zone
	)
	// Solr truncates the sub-second part (757747 -> 757); rounding would drift
	// the two endpoints apart by a millisecond again.
	if got, ok := normalizeModifiedTimestamp(showForm); !ok || got != searchForm {
		t.Errorf("normalizeModifiedTimestamp(%q) = %q, %v, want %q, true", showForm, got, ok, searchForm)
	}

	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"search form", searchForm, searchForm, true},
		{"no fraction", "2025-02-19T12:35:21", "2025-02-19T12:35:21.000Z", true},
		{"offset zone", "2025-02-19T13:35:21.757+01:00", searchForm, true},
		{"garbage", "not-a-time", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeModifiedTimestamp(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("normalizeModifiedTimestamp(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
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
	if err := c.put("my-pkg", "2025-02-19T12:35:21.757747", []byte(`{"result":"x"}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	body, ok := c.get("my-pkg", "2025-02-19T12:35:21.757Z")
	if !ok || string(body) != `{"result":"x"}` {
		t.Fatalf("the probe's spelling must hit the stored entry, got ok=%v body=%q", ok, body)
	}
	if _, ok := c.get("my-pkg", "2025-02-19T12:35:21.758Z"); ok {
		t.Error("a different instant must still miss")
	}

	// The reverse: what was stored is already canonical, so only the get side can
	// still bridge to the document's own spelling.
	if err := c.put("rev-pkg", "2025-02-19T12:35:21.757Z", []byte(`{"result":"y"}`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	body, ok = c.get("rev-pkg", "2025-02-19T12:35:21.757747")
	if !ok || string(body) != `{"result":"y"}` {
		t.Fatalf("the document's spelling must hit the entry stored from the probe's, got ok=%v body=%q", ok, body)
	}
}

// TestResultCache_UnreadableEntryIsMiss covers the failure between the name that
// decides freshness and the read that serves it: an entry whose name is right
// but whose bytes the read cannot deliver is a miss, never an error and never a
// partial body.
func TestResultCache_UnreadableEntryIsMiss(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	c := newTestCache(t, 10, 0)
	if err := c.put("pkg", modifiedFixture, []byte(bodyFixture)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.entryPath("pkg", modifiedFixture), 0o000); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.get("pkg", modifiedFixture); ok {
		t.Error("an unreadable entry must be a miss, not an error or a hit")
	}
}

// TestResultCache_CorruptEntryIsMiss pins the only validation a body still gets.
// It is served unparsed - that is the point of storing it verbatim - so a file
// that is not the JSON object a formatter wrote must be recognized here and
// answered with a miss; handing it to the client would turn a damaged cache into
// a broken response.
func TestResultCache_CorruptEntryIsMiss(t *testing.T) {
	c := newTestCache(t, 10, 0)
	if err := c.put("pkg", modifiedFixture, []byte(bodyFixture)); err != nil {
		t.Fatal(err)
	}
	path := c.entryPath("pkg", modifiedFixture)

	for _, content := range []string{"not json", ""} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.get("pkg", modifiedFixture); ok {
			t.Errorf("an entry holding %q must be a miss", content)
		}
	}
}

// The three operations a request can pay the cache for, measured on the shape a
// running server has: a body the size a large package produces, and an entries
// dir holding a realistic number of other packages (has() and prune() both walk
// it, get() and the write itself do not).
const benchEntryCount = 100

// benchBody stands in for a formatter-produced result body. Entries hold it
// verbatim, so this is exactly what a hit reads and a put writes.
func benchBody() []byte {
	const size = 2 << 20
	body := make([]byte, 0, size)
	body = append(body, `{"scan_result":"`...)
	for len(body) < size-2 {
		body = append(body, 'x')
	}
	return append(body, '"', '}')
}

func newBenchCache(b *testing.B) *resultCache {
	b.Helper()
	c, err := newResultCache(b.TempDir(), 500, 0)
	if err != nil {
		b.Fatalf("newResultCache: %v", err)
	}
	// The other packages' entries are only ever listed by name, so they carry
	// the smallest body an entry can have.
	for i := range benchEntryCount {
		if err := c.put(fmt.Sprintf("bench-other-%d", i), modifiedFixture, []byte("{}")); err != nil {
			b.Fatalf("seeding entry %d: %v", i, err)
		}
	}
	return c
}

func BenchmarkResultCacheGet(b *testing.B) {
	c := newBenchCache(b)
	if err := c.put("bench-pkg", modifiedFixture, benchBody()); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, ok := c.get("bench-pkg", modifiedFixture); !ok {
			b.Fatal("expected a hit")
		}
	}
}

func BenchmarkResultCacheHas(b *testing.B) {
	c := newBenchCache(b)
	if err := c.put("bench-pkg", modifiedFixture, benchBody()); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if !c.has("bench-pkg") {
			b.Fatal("expected an entry")
		}
	}
}

func BenchmarkResultCachePut(b *testing.B) {
	c := newBenchCache(b)
	body := benchBody()

	b.ReportAllocs()
	for b.Loop() {
		if err := c.put("bench-pkg", modifiedFixture, body); err != nil {
			b.Fatal(err)
		}
	}
}
