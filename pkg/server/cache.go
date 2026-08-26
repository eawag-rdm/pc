package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// resultCache is the per-package on-disk cache of successful analyze response
// bodies (spec: buffer repeat requests for unchanged packages). Freshness is
// decided against the CKAN package's metadata_modified timestamp as either
// endpoint reports it (package_show at put, the package_search probe or
// package_show at get) - CKAN bumps it on every dataset or resource change, so
// no extra CKAN (activity log) call is needed.
//
// Layout: the cache owns the "entries" subdirectory of the configured dir and
// nothing else - one JSON file per package, <dir>/entries/<package_id>.json.
// The configured dir itself stays the operator's (it is a mounted volume): the
// server reserves the name "entries" inside it, everything else is never
// touched. The package id is validated against the same grammar the analyze
// endpoint enforces before it is ever used as a filename, so no traversal is
// possible. Each entry file is self-contained (metadata_modified, cached_at,
// body).
//
// Invalidation has exactly two levers: the entries subdirectory is removed and
// recreated at startup (clearEntriesDir - a failure there aborts the boot),
// and within a run an entry is fresh only while the live metadata_modified
// still matches. A changed config or binary always means a restart, so the
// wipe covers both - nothing cached can predate the running process.
//
// Invariant: one server process per resultCacheDir. A second instance would
// wipe the first one's entries at its boot; this is a single-instance service.
//
// Writes happen only after a completed analysis and go through a same-dir
// temp file + rename, so a crash can never leave a torn entry. Every call
// except the boot-time clearEntriesDir is made under the handler's analysisMu,
// so there is no concurrent access; the methods are nil-safe so a nil
// *resultCache simply disables caching.
type resultCache struct {
	dir        string        // <resultCacheDir>/entries, owned wholesale by the cache
	maxEntries int           // evict oldest (mtime) beyond this; <= 0 disables the bound
	maxAge     time.Duration // entries older than this are misses; 0 disables the TTL

	// now is injectable so tests can drive TTL expiry.
	now func() time.Time
}

// cacheEntry is the on-disk format of one cached analysis. It carries no
// config or version identity: the startup wipe guarantees every entry the
// process reads was written by the process itself.
type cacheEntry struct {
	// MetadataModified is the canonical (millisecond-UTC) form of the CKAN
	// metadata_modified the analysis saw. The entry is fresh while the live
	// value normalizes to the same string.
	MetadataModified string    `json:"metadata_modified"`
	CachedAt         time.Time `json:"cached_at"`
	// Body is the formatted analysis response WITHOUT request_id (that is
	// injected per request when serving).
	Body string `json:"body"`
}

// entriesDirName is the cache-owned subdirectory of the configured cache dir.
// Keeping entries out of the dir root lets the startup wipe remove the whole
// subtree instead of guessing from filenames which files are ours - operator
// files in the mounted volume root are never at risk.
const entriesDirName = "entries"

// clearEntriesDir removes and recreates entriesDir, so a restart is a full
// cache invalidation (config and binary changes both require one). A failure
// must abort the boot: the server may not serve entries it cannot prove the
// running process wrote. The name is reserved for the cache, but only a real
// directory is ever deleted - anything else there is reported, not removed.
func clearEntriesDir(entriesDir string) error {
	// Lstat, not Stat: a symlink parked under the reserved name must neither be
	// followed nor unlinked. A failing Lstat (not-exist, unreadable parent) falls
	// through - RemoveAll/MkdirAll then produce the actionable error.
	if fi, err := os.Lstat(entriesDir); err == nil && !fi.Mode().IsDir() {
		return fmt.Errorf("cannot clear result cache entries dir %s: not a directory (the name %q is reserved for the result cache; move it aside)", entriesDir, entriesDirName)
	}
	err := os.RemoveAll(entriesDir)
	if err == nil {
		err = os.MkdirAll(entriesDir, 0o700)
	}
	if err != nil {
		return fmt.Errorf("cannot clear result cache entries dir %s: %v (check write access; read-only mount or changed ownership?)", entriesDir, err)
	}
	return nil
}

// newResultCache builds a cache under dir, clearing the entries of the
// previous run first. An empty dir disables caching (returns nil, no error).
// A clearing failure is returned rather than logged: the caller turns it into
// a hard startup failure.
func newResultCache(dir string, maxEntries int, maxAge time.Duration) (*resultCache, error) {
	if dir == "" {
		return nil, nil
	}
	entriesDir := filepath.Join(dir, entriesDirName)
	if err := clearEntriesDir(entriesDir); err != nil {
		return nil, err
	}
	return &resultCache{
		dir:        entriesDir,
		maxEntries: maxEntries,
		maxAge:     maxAge,
		now:        time.Now,
	}, nil
}

// entryPath maps a package id to its file in the entries subdir, or "" when
// the id is not safe to use as a filename. The analyze endpoint validates the id
// against packageIDPattern before the cache is touched; re-checking here is
// defence-in-depth (the grammar admits no '/', '.' or path metacharacters).
func (c *resultCache) entryPath(packageID string) string {
	if !packageIDPattern.MatchString(packageID) {
		return ""
	}
	return filepath.Join(c.dir, packageID+".json")
}

// normalizeModifiedTimestamp canonicalizes a CKAN metadata_modified string to
// millisecond-precision UTC, so that the package_search (Solr: milliseconds and
// a trailing Z) and package_show (Python isoformat: microseconds, no zone)
// spellings of the same instant compare equal. Solr truncates the sub-second
// part rather than rounding it, which is what the .000 verb below does too.
// Input that parses as neither form is returned unchanged: the comparison then
// degrades to the byte equality it had before, never to a false hit. Accepted:
// two changes to one package within the same millisecond canonicalize equal and
// the first one's cached body is served - the probe side carries no finer
// resolution to tell them apart.
func normalizeModifiedTimestamp(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// The isoformat side carries no zone at all; CKAN writes it in UTC,
		// which is also what a zone-less layout yields.
		t, err = time.Parse("2006-01-02T15:04:05.999999999", s)
		if err != nil {
			return s
		}
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// has reports whether an entry file exists for packageID, without reading or
// parsing it. A false positive (an entry that turns out corrupt, stale or
// expired) costs nothing but the probe; get() goes on to reject it. A stat
// error - permissions on the entries dir, say - deliberately reads as "no
// entry" and writes no log line: probes are then skipped silently, while the
// same breakage still surfaces on the write path as result_cache_write_failed.
func (c *resultCache) has(packageID string) bool {
	if c == nil {
		return false
	}
	path := c.entryPath(packageID)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// get returns the cached body for packageID if the entry exists, matches the
// live metadataModified, and is within the TTL. An empty metadataModified
// never hits: without the freshness signal a stale result could be served
// indefinitely.
func (c *resultCache) get(packageID, metadataModified string) (string, bool) {
	if c == nil || metadataModified == "" {
		return "", false
	}
	// The live value arrives in whichever spelling its CKAN endpoint uses - the
	// freshness probe's and the fetched document's differ - while put stored
	// the canonical one, so the compare below runs on canonical forms.
	metadataModified = normalizeModifiedTimestamp(metadataModified)
	path := c.entryPath(packageID)
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var e cacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", false
	}
	if e.MetadataModified != metadataModified {
		return "", false
	}
	if c.maxAge > 0 && c.now().Sub(e.CachedAt) > c.maxAge {
		return "", false
	}
	return e.Body, true
}

// put stores body as the cached analysis for packageID and enforces the
// entry bound. Failures are returned for logging; caching is best-effort and
// must never fail the request.
func (c *resultCache) put(packageID, metadataModified, body string) error {
	if c == nil || metadataModified == "" {
		return nil
	}
	path := c.entryPath(packageID)
	if path == "" {
		return fmt.Errorf("package id %q is not cacheable", packageID)
	}
	raw, err := json.Marshal(cacheEntry{
		// Stored canonical: the entry is looked up with a timestamp that may
		// come from either CKAN endpoint.
		MetadataModified: normalizeModifiedTimestamp(metadataModified),
		CachedAt:         c.now(),
		Body:             body,
	})
	if err != nil {
		return err
	}
	// Same-dir temp + rename: the entry is either the old file or the complete
	// new one, never a torn write.
	tmp, err := os.CreateTemp(c.dir, "put-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	c.prune()
	return nil
}

// prune enforces maxEntries by removing the oldest-modified entries. Called
// after each put; best-effort (an unreadable dir just skips pruning).
func (c *resultCache) prune() {
	if c.maxEntries <= 0 {
		return
	}
	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type fileAge struct {
		name string
		mod  time.Time
	}
	var files []fileAge
	for _, de := range dirEntries {
		// The subdir holds only our files, so a .json filter is enough - it
		// keeps a crashed put's leftover put-*.tmp from being counted or
		// evicted as an entry.
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		files = append(files, fileAge{name: de.Name(), mod: info.ModTime()})
	}
	if len(files) <= c.maxEntries {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files[:len(files)-c.maxEntries] {
		os.Remove(filepath.Join(c.dir, f.name))
	}
}
