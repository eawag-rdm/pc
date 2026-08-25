package structs

// ArchiveNameCollector carries the member list one archive walk saw, so the
// member-name checks can run on it instead of decompressing the archive a
// second time. An under-cap .tar.gz is otherwise unpacked twice: once to list
// its members, once to scan their content.
//
// No locking: exactly one walk fills a collector, and BeginFill enforces it.
// Nothing here is safe to call from a second goroutine.
type ArchiveNameCollector struct {
	names     []File
	max       int
	count     int
	claimed   bool
	finished  bool
	truncated bool
	refused   bool
}

// NewArchiveNameCollector returns a collector that keeps at most max member
// names; see Note for what max counts.
func NewArchiveNameCollector(max int) *ArchiveNameCollector {
	return &ArchiveNameCollector{max: max}
}

// BeginFill claims the single fill. The first caller gets true and owns the
// collector; every later caller gets false and marks the collector refused, so
// a second walk fails closed here instead of handing out a list neither of them
// saw whole (dispatch falls back to the listing walk). Dispatch hands a
// collector to a single filler in the first place, so this is the backstop
// rather than the mechanism.
func (c *ArchiveNameCollector) BeginFill() bool {
	if c.claimed {
		c.refused = true
		return false
	}
	c.claimed = true
	return true
}

// Note records one member HEADER, called once per header the walk sees -
// directories and zero-size members included, because that is what the name
// checks see too.
//
// EVERY call counts against max, not just the ones that fit: a crafted ~4.7 MB
// tar.gz holds ~3.9M zero-size members within the walk budget, so a cap that
// only counted kept names would leave the collector's memory unbounded. Past
// max the collected names are released and nothing further is kept.
func (c *ArchiveNameCollector) Note(f File) {
	c.count++
	if c.count > c.max {
		c.truncated = true
		c.names = nil
		return
	}
	if c.names == nil {
		// Sized for the common archive rather than for the cap: growing from nil
		// costs a copy per doubling, sizing to a millions-wide cap costs the
		// whole array up front.
		c.names = make([]File, 0, min(c.max, 64))
	}
	c.names = append(c.names, f)
}

// Finish marks the walk as having reached a clean end of archive. A walk that
// stopped anywhere else (walk budget, member cap, context cancellation, read
// error) simply never calls it, so what it collected stays unusable.
func (c *ArchiveNameCollector) Finish() {
	c.finished = true
}

// Result returns the collected members and whether they are the archive's
// complete member list. ok only when a fill finished and was neither truncated
// nor refused; on a false ok the names are whatever the partial walk left and
// no caller may check them.
func (c *ArchiveNameCollector) Result() ([]File, bool) {
	return c.names, c.finished && !c.truncated && !c.refused
}
