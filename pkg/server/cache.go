package server

import (
	"crypto/sha256"
	"encoding/hex"
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
// keyed on the CKAN package's metadata_modified timestamp, which the single
// package_show call already carries — CKAN bumps it on every dataset or
// resource change, so no extra CKAN (activity log) call is needed.
//
// Layout: one JSON file per package, <dir>/<package_id>.json. The package id
// is validated against the same grammar the analyze endpoint enforces before
// it is ever used as a filename, so no traversal is possible. Each file is
// self-contained (fingerprint, metadata_modified, cached_at, body): a config
// or version change makes every existing file fail its fingerprint check on
// read — no wipe step, stale files are simply overwritten by the next
// analysis or pruned by eviction.
//
// Writes happen only after a completed analysis and go through a same-dir
// temp file + rename, so a crash can never leave a torn entry. All calls are
// made under the handler's analysisMu, so there is no concurrent access; the
// methods are nil-safe so a nil *resultCache simply disables caching.
type resultCache struct {
	dir         string
	fingerprint string
	maxEntries  int           // evict oldest (mtime) beyond this; <= 0 disables the bound
	maxAge      time.Duration // entries older than this are misses; 0 disables the TTL

	// now is injectable so tests can drive TTL expiry.
	now func() time.Time
}

// cacheEntry is the on-disk format of one cached analysis.
type cacheEntry struct {
	// Fingerprint identifies the config + server version the body was produced
	// with; a mismatch invalidates the entry (checks may have changed).
	Fingerprint string `json:"fingerprint"`
	// MetadataModified is the CKAN metadata_modified the analysis saw. The
	// entry is fresh only while the live package reports the same value.
	MetadataModified string    `json:"metadata_modified"`
	CachedAt         time.Time `json:"cached_at"`
	// Body is the formatted analysis response WITHOUT request_id (that is
	// injected per request when serving).
	Body string `json:"body"`
}

// newResultCache builds a cache rooted at dir, creating the directory if
// needed. An empty dir disables caching (returns nil, no error).
func newResultCache(dir, fingerprint string, maxEntries int, maxAge time.Duration) (*resultCache, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create result cache dir %q: %w", dir, err)
	}
	return &resultCache{
		dir:         dir,
		fingerprint: fingerprint,
		maxEntries:  maxEntries,
		maxAge:      maxAge,
		now:         time.Now,
	}, nil
}

// configFingerprint derives the cache-invalidation fingerprint from the raw
// config file bytes and the server version: changing either means cached
// results may no longer match what the checks would produce today.
func configFingerprint(configBytes []byte) string {
	sum := sha256.Sum256(append(configBytes, []byte(serverVersion)...))
	return hex.EncodeToString(sum[:])
}

// entryPath maps a package id to its cache file, or "" when the id is not
// safe to use as a filename. The analyze endpoint already validates the id
// against packageIDPattern before the cache is touched; re-checking here is
// defence-in-depth (the grammar admits no '/', '.' or path metacharacters).
func (c *resultCache) entryPath(packageID string) string {
	if !packageIDPattern.MatchString(packageID) {
		return ""
	}
	return filepath.Join(c.dir, packageID+".json")
}

// get returns the cached body for packageID if the entry exists, carries the
// current fingerprint, matches the live metadataModified, and is within the
// TTL. An empty metadataModified never hits: without the freshness signal a
// stale result could be served indefinitely.
func (c *resultCache) get(packageID, metadataModified string) (string, bool) {
	if c == nil || metadataModified == "" {
		return "", false
	}
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
	if e.Fingerprint != c.fingerprint || e.MetadataModified != metadataModified {
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
		Fingerprint:      c.fingerprint,
		MetadataModified: metadataModified,
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
