package readers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	pdfium "github.com/klippa-app/go-pdfium"
	"github.com/stretchr/testify/assert"
)

// TestMain asserts the lazy-init guarantee mechanically before ANY test in
// the package runs (immune to -shuffle and -run selections, unlike the
// former must-stay-first test): a run that never sees a PDF must never pay
// for the wasm runtime. Benchmarks run after tests, so committed
// Benchmark* functions do not violate this.
func TestMain(m *testing.M) {
	if pdfRuntimeInitialized() {
		fmt.Fprintln(os.Stderr, "FAIL: PDF runtime initialized before any test ran - lazy-init guarantee broken (package-level init touched the pool)")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// writeMinimalPDF builds a valid single-font PDF with one page per text,
// recording object offsets while writing so the xref table is always correct
// (hand-written fixtures rot invisibly: PDFium silently rebuilds broken
// xrefs, so a stale fixture still parses but exercises the recovery path).
// Texts must not contain (, ) or backslashes.
func writeMinimalPDF(texts ...string) []byte {
	var buf bytes.Buffer
	offsets := []int{0} // object 0 is the free head
	writeObj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%s\n", body)
	}

	buf.WriteString("%PDF-1.4\n")

	n := len(texts)
	fontObj := 3 + 2*n
	kids := make([]string, n)
	for i := range texts {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	writeObj("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj")
	writeObj(fmt.Sprintf("2 0 obj << /Type /Pages /Kids [%s] /Count %d >> endobj", strings.Join(kids, " "), n))
	for i, text := range texts {
		pageObj := 3 + 2*i
		contentObj := pageObj + 1
		writeObj(fmt.Sprintf("%d 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >> endobj", pageObj, contentObj, fontObj))
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		writeObj(fmt.Sprintf("%d 0 obj << /Length %d >> stream\n%s\nendstream endobj", contentObj, len(stream), stream))
	}
	writeObj(fmt.Sprintf("%d 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj", fontObj))

	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xrefStart)
	return buf.Bytes()
}

var testPDFLimits = PDFLimits{MaxFileBytes: 5 * 1024 * 1024, MaxPages: 500, MaxTextBytes: 10 * 1024 * 1024, Timeout: 30 * time.Second}

func TestReadPDFExtractsPerPageText(t *testing.T) {
	data := writeMinimalPDF("alpha secret on page one", "beta token on page two")
	pages, truncated, err := ReadPDF(data, testPDFLimits)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, pages, 2)
	assert.Contains(t, string(pages[0]), "alpha secret")
	assert.Contains(t, string(pages[1]), "beta token")
}

func TestReadPDFPageCapRejectsWholeDocument(t *testing.T) {
	// All-or-nothing: over the ceiling, NOTHING is extracted - a partial
	// scan would report a long document as checked when most of it wasn't.
	data := writeMinimalPDF("page one text", "page two text", "page three hidden", "page four hidden", "page five hidden")
	limits := testPDFLimits
	limits.MaxPages = 2
	pages, truncated, err := ReadPDF(data, limits)
	assert.ErrorIs(t, err, ErrPDFTooManyPages)
	assert.Nil(t, pages, "no page may be extracted from an over-length document")
	assert.False(t, truncated)

	// Exactly at the ceiling is still scanned in full.
	limits.MaxPages = 5
	pages, truncated, err = ReadPDF(data, limits)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, pages, 5)
}

func TestReadPDFFileSizeGate(t *testing.T) {
	data := writeMinimalPDF("some text")
	limits := testPDFLimits
	limits.MaxFileBytes = int64(len(data)) - 1
	_, _, err := ReadPDF(data, limits)
	assert.ErrorIs(t, err, ErrPDFTooLarge)

	limits.MaxFileBytes = int64(len(data)) // exactly at the gate passes
	pages, _, err := ReadPDF(data, limits)
	assert.NoError(t, err)
	assert.Len(t, pages, 1)
}

func TestReadPDFTextCap(t *testing.T) {
	data := writeMinimalPDF("word " + strings.Repeat("filler ", 50))
	limits := testPDFLimits
	limits.MaxTextBytes = 16
	pages, truncated, err := ReadPDF(data, limits)
	assert.NoError(t, err)
	assert.True(t, truncated)
	total := 0
	for _, p := range pages {
		total += len(p)
	}
	assert.LessOrEqual(t, total, 16+4, "extraction must stop at the byte cap (UTF-8 slack allowed)")
}

func TestReadPDFDamagedMiddlePageKeepsIndexes(t *testing.T) {
	// Middle Kids entry points at a nonexistent object. Whether PDFium
	// surfaces that as a load error or as an empty page, the damaged page
	// must keep a placeholder so later pages still cite the right "page N".
	data := writeMinimalPDF("first page text", "second page text", "third page text")
	data = bytes.Replace(data, []byte("/Kids [3 0 R 5 0 R 7 0 R]"), []byte("/Kids [3 0 R 99 0 R 7 0 R]"), 1)
	pages, truncated, err := ReadPDF(data, testPDFLimits)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, pages, 3)
	assert.Empty(t, pages[1])
	assert.Contains(t, string(pages[2]), "third page", "damaged page 2 must not shift page 3's index")
}

func TestReadPDFMalformed(t *testing.T) {
	// Magic present, body garbage: must error cleanly, never hang or crash.
	junk := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x42, 0x00, 0x13}, 4096)...)
	_, _, err := ReadPDF(junk, testPDFLimits)
	assert.Error(t, err)

	_, _, err = ReadPDF([]byte("not a pdf at all"), testPDFLimits)
	assert.Error(t, err)
}

func TestReadPDFFailClosedLimits(t *testing.T) {
	data := writeMinimalPDF("text")
	_, _, err := ReadPDF(data, PDFLimits{MaxFileBytes: 1 << 20, MaxPages: 0, MaxTextBytes: 1024})
	assert.Error(t, err)
	_, _, err = ReadPDF(data, PDFLimits{MaxFileBytes: 1 << 20, MaxPages: 10, MaxTextBytes: 0})
	assert.Error(t, err)
	_, _, err = ReadPDF(data, PDFLimits{MaxFileBytes: 0, MaxPages: 10, MaxTextBytes: 1024})
	assert.Error(t, err, "a zero size limit must fail closed, never mean unlimited")
}

func TestReadPDFTooLargeFailsFast(t *testing.T) {
	// The zeroed pages are never touched: ReadPDF must reject on length
	// alone, before any runtime init or extraction work.
	data := make([]byte, MaxPDFInputBytes+1)
	_, _, err := ReadPDF(data, testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFTooLarge)
}

// The two shapes that matter: per-document pool churn dominates the
// single page, extraction throughput dominates the 50 pages. Benchmarks
// run after tests, so the TestMain lazy-init assert is unaffected.
func benchmarkReadPDF(b *testing.B, pageCount int) {
	texts := make([]string, pageCount)
	for i := range texts {
		texts[i] = strings.Repeat("benchmark page text with several words ", 20)
	}
	data := writeMinimalPDF(texts...)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ReadPDF(data, testPDFLimits); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadPDFSinglePage(b *testing.B) { benchmarkReadPDF(b, 1) }
func BenchmarkReadPDF50Pages(b *testing.B)    { benchmarkReadPDF(b, 50) }

// fakePDFPool stands in for the wasm pool: the retry test must not build a
// real runtime (and must not hand the shared one to later tests).
type fakePDFPool struct{}

func (*fakePDFPool) GetInstance(time.Duration) (pdfium.Pdfium, error) { return nil, nil }

// Errors rather than handing out a nil instance: if a test ever leaked this
// fake past its cleanup, a real extraction fails loudly instead of nil-deref.
func (*fakePDFPool) GetInstanceWithContext(context.Context) (pdfium.Pdfium, error) {
	return nil, errors.New("fake pool has no instances")
}
func (*fakePDFPool) Close() error { return nil }

// savePDFRuntime hijacks the package-level runtime the real-PDF tests share,
// so its callers must never run parallel. Every field this test touches is
// saved and restored one by one (the struct holds a mutex, so it cannot be
// copied), under that mutex.
func savePDFRuntime(t *testing.T) {
	t.Helper()
	savedInit, savedBase := initFn, pdfInitCooldown
	savedReady := pdfRuntime.ready.Load()
	savedInitialized := pdfRuntime.initialized.Load()
	pdfRuntime.mu.Lock()
	savedPool, savedErr := pdfRuntime.pool, pdfRuntime.err
	savedAttempt, savedCooldown := pdfRuntime.lastAttempt, pdfRuntime.cooldown
	pdfRuntime.mu.Unlock()
	t.Cleanup(func() {
		initFn, pdfInitCooldown = savedInit, savedBase
		pdfRuntime.mu.Lock()
		pdfRuntime.pool, pdfRuntime.err = savedPool, savedErr
		pdfRuntime.lastAttempt, pdfRuntime.cooldown = savedAttempt, savedCooldown
		pdfRuntime.mu.Unlock()
		// After pool: ready points at pdfRuntime.pool.
		pdfRuntime.ready.Store(savedReady)
		pdfRuntime.initialized.Store(savedInitialized)
	})
}

// resetPDFRuntime starts from an uninitialized runtime whatever earlier tests
// left behind.
func resetPDFRuntime() {
	pdfRuntime.ready.Store(nil)
	pdfRuntime.mu.Lock()
	defer pdfRuntime.mu.Unlock()
	pdfRuntime.pool, pdfRuntime.err = nil, nil
	pdfRuntime.lastAttempt, pdfRuntime.cooldown = time.Time{}, 0
}

// pdfRuntimeSnapshot reads the memoized failure state under the mutex.
func pdfRuntimeSnapshot() (time.Duration, error) {
	pdfRuntime.mu.Lock()
	defer pdfRuntime.mu.Unlock()
	return pdfRuntime.cooldown, pdfRuntime.err
}

// expirePDFCooldown rewinds lastAttempt past the current cooldown; the clock
// is never waited on.
func expirePDFCooldown() {
	pdfRuntime.mu.Lock()
	defer pdfRuntime.mu.Unlock()
	pdfRuntime.lastAttempt = time.Now().Add(-2 * pdfRuntime.cooldown)
}

func setPDFCooldown(d time.Duration) {
	pdfRuntime.mu.Lock()
	defer pdfRuntime.mu.Unlock()
	pdfRuntime.cooldown = d
}

func TestPDFRuntimeRetriesFailedInitAfterCooldown(t *testing.T) {
	savePDFRuntime(t)
	resetPDFRuntime()
	// Well under the cap so the doubling is observable.
	pdfInitCooldown = 10 * time.Second

	calls := 0
	initErr := errors.New("wasm init failed")
	initFn = func() (pdfium.Pool, error) {
		calls++
		return nil, initErr
	}

	_, err := pdfPool()
	assert.ErrorIs(t, err, initErr)
	assert.Equal(t, 1, calls)
	cooldown, _ := pdfRuntimeSnapshot()
	assert.Equal(t, pdfInitCooldown, cooldown)
	assert.True(t, pdfRuntimeInitialized(), "a failed attempt still counts as started")

	// Inside the cooldown: same error, no per-file init retry storm.
	_, err = pdfPool()
	assert.ErrorIs(t, err, initErr)
	assert.Equal(t, 1, calls, "init must not be retried inside the cooldown")

	// Callers keep seeing the runtime sentinel, unchanged by the retry logic.
	_, _, rerr := ReadPDF(writeMinimalPDF("some text"), testPDFLimits)
	assert.ErrorIs(t, rerr, ErrPDFRuntime)
	assert.Equal(t, 1, calls)

	// Cooldown elapsed: exactly one more attempt, and the backoff doubles.
	expirePDFCooldown()
	_, err = pdfPool()
	assert.ErrorIs(t, err, initErr)
	assert.Equal(t, 2, calls)
	cooldown, _ = pdfRuntimeSnapshot()
	assert.Equal(t, 2*pdfInitCooldown, cooldown, "backoff doubles per consecutive failure")

	// The backoff is capped, not unbounded.
	setPDFCooldown(pdfInitCooldownMax)
	expirePDFCooldown()
	_, err = pdfPool()
	assert.ErrorIs(t, err, initErr)
	assert.Equal(t, 3, calls)
	cooldown, _ = pdfRuntimeSnapshot()
	assert.Equal(t, pdfInitCooldownMax, cooldown)

	// A retry that succeeds serves the pool, clears the stale error and
	// disarms the backoff.
	want := &fakePDFPool{}
	initFn = func() (pdfium.Pool, error) {
		calls++
		return want, nil
	}
	expirePDFCooldown()
	pool, err := pdfPool()
	assert.NoError(t, err)
	assert.Same(t, want, pool)
	cooldown, memo := pdfRuntimeSnapshot()
	assert.NoError(t, memo, "a successful retry must clear the memoized failure")
	assert.Zero(t, cooldown, "a successful retry must reset the backoff")
	assert.Equal(t, 4, calls)

	// Initialized: pure fast path from here on.
	pool, err = pdfPool()
	assert.NoError(t, err)
	assert.Same(t, want, pool)
	assert.Equal(t, 4, calls, "an initialized runtime must never re-init")
}

func TestPDFRuntimeNilPoolIsAFailure(t *testing.T) {
	// An init returning (nil, nil) must never be published: readers would
	// deref a nil pool. It is memoized and backed off like any other failure.
	savePDFRuntime(t)
	resetPDFRuntime()
	pdfInitCooldown = 10 * time.Second

	calls := 0
	initFn = func() (pdfium.Pool, error) {
		calls++
		return nil, nil
	}

	pool, err := pdfPool()
	assert.Nil(t, pool)
	assert.ErrorContains(t, err, "no pool")
	assert.Equal(t, 1, calls)
	assert.Nil(t, pdfRuntime.ready.Load(), "a nil pool must never be published")

	cooldown, memo := pdfRuntimeSnapshot()
	assert.Equal(t, err, memo, "the synthesized error must be memoized")
	assert.Equal(t, pdfInitCooldown, cooldown, "a nil pool must arm the backoff")

	// Memoized: inside the cooldown the same error is served, no retry.
	pool, again := pdfPool()
	assert.Nil(t, pool)
	assert.Equal(t, err, again)
	assert.Equal(t, 1, calls, "a nil pool must not be retried inside the cooldown")
}

func TestReadPDFConcurrentBatch(t *testing.T) {
	// Double-checked lazy init plus pool under concurrency: more goroutines
	// than pool instances.
	data := writeMinimalPDF("concurrent page")
	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			pages, _, err := ReadPDF(data, testPDFLimits)
			if err == nil && (len(pages) != 1 || !strings.Contains(string(pages[0]), "concurrent")) {
				err = fmt.Errorf("bad extraction: %q", pages)
			}
			done <- err
		}()
	}
	for i := 0; i < 12; i++ {
		assert.NoError(t, <-done)
	}
}
