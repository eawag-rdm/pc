package server

import (
	"errors"
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
// nothing else - one file per package,
// <dir>/entries/<package_id>.<timestamp-token>.json, holding the response body
// verbatim. The freshness key is the FILENAME: a request names the only file
// that may answer it, so a hit is one read and a miss a failed open, with no
// parsing of a body that runs to megabytes. has() holds only the package id and
// can name no file, so it lists the entries directory instead - bounded by
// maxEntries. The file's mtime is the TTL clock, stat'ed only where a TTL is
// set.
// The configured dir itself stays the operator's (it is a mounted volume): the
// server reserves the name "entries" inside it, everything else is never
// touched. The package id is validated against the same grammar the analyze
// endpoint enforces before it is ever used as a filename, so no traversal is
// possible.
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

// entryPath maps a package id and the metadata_modified an analysis was made
// at to their file in the entries subdir, or "" when the pair can name no file:
// an id that is not safe as a filename, or a timestamp that parses as neither
// CKAN spelling. The timestamp is canonicalized first (the probe and the
// document spell one instant differently), so the name a request builds is the
// name put wrote: comparing freshness is comparing filenames. A timestamp that
// canonicalizes to nothing is therefore not cached at all - such a package
// misses on every request, which costs a full analysis and nothing else. The
// analyze endpoint validates the id against packageIDPattern before the cache is
// touched; re-checking here is defence-in-depth (the grammar admits no '/', '.'
// or path metacharacters).
func (c *resultCache) entryPath(packageID, metadataModified string) string {
	if !packageIDPattern.MatchString(packageID) {
		return ""
	}
	canonical, ok := normalizeModifiedTimestamp(metadataModified)
	if !ok {
		return ""
	}
	return filepath.Join(c.dir, packageID+"."+modifiedTimestampToken(canonical)+".json")
}

// normalizeModifiedTimestamp canonicalizes a CKAN metadata_modified string to
// millisecond-precision UTC, so that the package_search (Solr: milliseconds and
// a trailing Z) and package_show (Python isoformat: microseconds, no zone)
// spellings of the same instant compare equal. Solr truncates the sub-second
// part rather than rounding it, which is what the .000 verb below does too.
// Input that parses as neither form reports ok=false and is refused: it names no
// entry, so nothing is cached under it. Accepted: two changes to one package
// within the same millisecond canonicalize equal and the first one's cached body
// is served - the probe side carries no finer resolution to tell them apart.
func normalizeModifiedTimestamp(s string) (string, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// The isoformat side carries no zone at all; CKAN writes it in UTC,
		// which is also what a zone-less layout yields.
		t, err = time.Parse("2006-01-02T15:04:05.999999999", s)
		if err != nil {
			return "", false
		}
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00"), true
}

// modifiedTimestampToken reduces a canonical timestamp to the token an entry
// filename carries, keeping its alphanumeric bytes and dropping every other one
// - that is what keeps the name path-safe, whatever it is handed. Only canonical
// input reaches here, and that layout is fixed-width, so the mapping is
// injective: two timestamps share a token only if they are the same instant.
func modifiedTimestampToken(canonical string) string {
	token := make([]byte, 0, len(canonical))
	for i := 0; i < len(canonical); i++ {
		switch b := canonical[i]; {
		case b >= '0' && b <= '9', b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z':
			token = append(token, b)
		}
	}
	return string(token)
}

// isEntryFile reports whether a name in the entries dir is an entry of the
// package whose filename prefix ("<id>.") is given. The prefix carries the
// separator, so one package id is never the start of another's. Callers hoist
// the prefix out of their loop and skip directories themselves.
func isEntryFile(name, prefix string) bool {
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".json")
}

// has reports whether a live entry exists for packageID, without reading one. It
// gates the freshness probe, and a false positive - an entry the probe then
// finds stale - costs nothing but that probe. The listing is unsorted: any match
// answers, so ordering the names buys nothing. A read error on the entries dir -
// permissions, say - deliberately reads as "no entry" and writes no log line:
// probes are then skipped silently, while the same breakage still surfaces on
// the write path as result_cache_write_failed.
func (c *resultCache) has(packageID string) bool {
	if c == nil {
		return false
	}
	dir, err := os.Open(c.dir)
	if err != nil {
		return false
	}
	dirEntries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return false
	}
	prefix := packageID + "."
	for _, de := range dirEntries {
		if de.IsDir() || !isEntryFile(de.Name(), prefix) {
			continue
		}
		// An entry past the TTL can never be served, so it must buy no probe.
		if c.maxAge > 0 {
			info, err := de.Info()
			if err != nil || c.now().Sub(info.ModTime()) > c.maxAge {
				continue
			}
		}
		return true
	}
	return false
}

// get returns the cached body for packageID if the entry stored for exactly
// this metadataModified exists and is within the TTL. The live value arrives in
// whichever spelling its CKAN endpoint uses - the freshness probe's and the
// fetched document's differ - and entryPath canonicalizes it, so freshness is
// decided by the filename alone: a hit reads the body and parses none of it, a
// miss finds no file to read. A metadataModified that is empty or that parses
// as neither spelling names no entry and never hits: without the freshness
// signal a stale result could be served indefinitely. The TTL is the one thing
// a stat can decide, so it is the only thing that pays for one; without a TTL
// the read alone tells a present entry from an absent one.
func (c *resultCache) get(packageID, metadataModified string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	path := c.entryPath(packageID, metadataModified)
	if path == "" {
		return nil, false
	}
	if c.maxAge > 0 {
		info, err := os.Stat(path)
		if err != nil || c.now().Sub(info.ModTime()) > c.maxAge {
			return nil, false
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	// An entry is served unparsed, so this is its whole validation: anything
	// that does not open a JSON object is torn or foreign, and a miss rewrites it.
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}
	return body, true
}

// errUncacheableTimestamp reports a metadata_modified that names no entry file
// - missing, or parsing as neither CKAN spelling. The analysis itself
// succeeded, so callers match it to log the fact rather than to report a
// failure.
var errUncacheableTimestamp = errors.New("metadata_modified is not cacheable")

// put stores body verbatim as the cached analysis for packageID and enforces
// the entry bound. Failures are returned for logging; caching is best-effort
// and must never fail the request.
func (c *resultCache) put(packageID, metadataModified string, body []byte) error {
	if c == nil {
		return nil
	}
	// The id is checked here rather than left to entryPath so that the two
	// reasons a path can come back empty stay apart: an id that cannot be a
	// filename is a caller error worth reporting, while a metadata_modified that
	// is missing or parses as neither spelling names no entry and comes back as
	// errUncacheableTimestamp - the analysis succeeded, and the next request for
	// that package runs again.
	if !packageIDPattern.MatchString(packageID) {
		return fmt.Errorf("package id %q is not cacheable", packageID)
	}
	path := c.entryPath(packageID, metadataModified)
	if path == "" {
		return errUncacheableTimestamp
	}
	// Same-dir temp + rename: the entry is either the old file or the complete
	// new one, never a torn write.
	tmp, err := os.CreateTemp(c.dir, "put-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
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
	c.removeSupersededEntries(packageID, filepath.Base(path))
	c.prune()
	return nil
}

// removeSupersededEntries deletes the package's other entry files - what it was
// cached as before this metadata_modified. Since the timestamp is part of the
// name, a new entry no longer overwrites the old one; without this the
// directory would keep a file per version of every package. Best-effort: a
// leftover only wastes space, and prune() still bounds the directory.
func (c *resultCache) removeSupersededEntries(packageID, keep string) {
	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	prefix := packageID + "."
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || name == keep || !isEntryFile(name, prefix) {
			continue
		}
		os.Remove(filepath.Join(c.dir, name))
	}
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
