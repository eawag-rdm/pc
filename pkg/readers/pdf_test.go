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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain asserts the lazy-init guarantee mechanically before ANY test in
// the package runs (immune to -shuffle and -run selections, unlike the
// former must-stay-first test): a run that never sees a PDF must never pay
// for the wasm runtime. Benchmarks run after tests, so committed
// Benchmark* functions do not violate this.
func TestMain(m *testing.M) {
	// The pool starts workers by re-executing this binary, which here is the
	// test binary: the child has to run a worker loop, not the suite again.
	// handlePDFStubWorker takes the children a test asked to misbehave,
	// HandlePDFWorkerSentinel every other one.
	handlePDFStubWorker()
	HandlePDFWorkerSentinel()
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
	pages, truncated, err := ReadPDF(context.Background(), data, testPDFLimits)
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
	pages, truncated, err := ReadPDF(context.Background(), data, limits)
	assert.ErrorIs(t, err, ErrPDFTooManyPages)
	assert.Nil(t, pages, "no page may be extracted from an over-length document")
	assert.False(t, truncated)

	// Exactly at the ceiling is still scanned in full.
	limits.MaxPages = 5
	pages, truncated, err = ReadPDF(context.Background(), data, limits)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, pages, 5)
}

func TestReadPDFFileSizeGate(t *testing.T) {
	data := writeMinimalPDF("some text")
	limits := testPDFLimits
	limits.MaxFileBytes = int64(len(data)) - 1
	_, _, err := ReadPDF(context.Background(), data, limits)
	assert.ErrorIs(t, err, ErrPDFTooLarge)

	limits.MaxFileBytes = int64(len(data)) // exactly at the gate passes
	pages, _, err := ReadPDF(context.Background(), data, limits)
	assert.NoError(t, err)
	assert.Len(t, pages, 1)
}

func TestReadPDFTextCap(t *testing.T) {
	data := writeMinimalPDF("word " + strings.Repeat("filler ", 50))
	limits := testPDFLimits
	limits.MaxTextBytes = 16
	pages, truncated, err := ReadPDF(context.Background(), data, limits)
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
	pages, truncated, err := ReadPDF(context.Background(), data, testPDFLimits)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, pages, 3)
	assert.Empty(t, pages[1])
	assert.Contains(t, string(pages[2]), "third page", "damaged page 2 must not shift page 3's index")
}

func TestReadPDFMalformed(t *testing.T) {
	// Magic present, body garbage: must error cleanly, never hang or crash.
	junk := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x42, 0x00, 0x13}, 4096)...)
	_, _, err := ReadPDF(context.Background(), junk, testPDFLimits)
	assert.Error(t, err)

	_, _, err = ReadPDF(context.Background(), []byte("not a pdf at all"), testPDFLimits)
	assert.Error(t, err)
}

func TestReadPDFFailClosedLimits(t *testing.T) {
	data := writeMinimalPDF("text")
	_, _, err := ReadPDF(context.Background(), data, PDFLimits{MaxFileBytes: 1 << 20, MaxPages: 0, MaxTextBytes: 1024})
	assert.Error(t, err)
	_, _, err = ReadPDF(context.Background(), data, PDFLimits{MaxFileBytes: 1 << 20, MaxPages: 10, MaxTextBytes: 0})
	assert.Error(t, err)
	_, _, err = ReadPDF(context.Background(), data, PDFLimits{MaxFileBytes: 0, MaxPages: 10, MaxTextBytes: 1024})
	assert.Error(t, err, "a zero size limit must fail closed, never mean unlimited")
}

func TestReadPDFTooLargeFailsFast(t *testing.T) {
	// The zeroed pages are never touched: ReadPDF must reject on length
	// alone, before any runtime init or extraction work.
	data := make([]byte, MaxPDFInputBytes+1)
	_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFTooLarge)
}

func TestReadPDFTimeoutDiscardsContent(t *testing.T) {
	// A 1 ns budget is spent before the worker reaches its first deadline
	// check, so the outcome is deterministic however the extraction is
	// scheduled - and it is the worker that ends it, inside the job, which is
	// why this costs no process.
	data := writeMinimalPDF("timeout page one", "timeout page two")
	limits := testPDFLimits
	limits.Timeout = time.Nanosecond
	pages, truncated, err := ReadPDF(context.Background(), data, limits)
	assert.ErrorIs(t, err, ErrPDFTimeout)
	assert.Nil(t, pages, "timeout must discard partial content (determinism)")
	assert.False(t, truncated)
}

// The three shapes that matter: per-job worker overhead dominates the single
// page, extraction throughput dominates the 50 pages, and the near-gate
// document is what the pipe costs on a file the size of the shipped admission
// limit. The allocation columns count the PARENT only - the extraction itself
// happens in another process. Benchmarks run after tests, so the TestMain
// lazy-init assert is unaffected.
func benchmarkReadPDF(b *testing.B, data []byte) {
	// Warm the pool outside the measurement: the first call starts a worker
	// process, which is not what any of these benchmarks is about.
	if _, _, err := ReadPDF(context.Background(), data, testPDFLimits); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ReadPDF(context.Background(), data, testPDFLimits); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkPDFPages(pageCount int) []byte {
	texts := make([]string, pageCount)
	for i := range texts {
		texts[i] = strings.Repeat("benchmark page text with several words ", 20)
	}
	return writeMinimalPDF(texts...)
}

func BenchmarkReadPDFSinglePage(b *testing.B) { benchmarkReadPDF(b, benchmarkPDFPages(1)) }
func BenchmarkReadPDF50Pages(b *testing.B)    { benchmarkReadPDF(b, benchmarkPDFPages(50)) }

func BenchmarkReadPDFNearGate(b *testing.B) {
	// ~1 MiB, the shipped maxPDFFileSize: the document the pipe has to carry
	// in full on a file the configuration still admits.
	const pageText = 24 * 1024
	pages := make([]string, 0, 48)
	for size := 0; size < 1<<20; size += pageText {
		pages = append(pages, strings.Repeat("near gate filler words ", pageText/23))
	}
	data := writeMinimalPDF(pages...)
	if len(data) < 1<<20 {
		b.Fatalf("fixture is %d bytes, want at least 1 MiB", len(data))
	}
	benchmarkReadPDF(b, data)
}

// unusedPDFPool stands in for the worker pool where a test must not extract
// anything: nothing is started until a job asks for a worker, so an unused pool
// costs two channels. It carries no binary to start, so one leaked past its
// cleanup fails the first job loudly instead of serving it silently.
func unusedPDFPool(t *testing.T) *pdfWorkerPool {
	t.Helper()
	pool := newPDFWorkerPool("", 1, testPDFPoolTimings())
	t.Cleanup(pool.Close)
	return pool
}

// readOutcome is one ReadPDF return, so a test can wait on the call with a
// bound instead of hanging the suite when a worker is never killed.
type readOutcome struct {
	pages     [][]byte
	truncated bool
	err       error
}

func readPDFAsync(ctx context.Context, data []byte, limits PDFLimits) <-chan readOutcome {
	done := make(chan readOutcome, 1)
	go func() {
		pages, truncated, err := ReadPDF(ctx, data, limits)
		done <- readOutcome{pages: pages, truncated: truncated, err: err}
	}()
	return done
}

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
	initFn = func() (*pdfWorkerPool, error) {
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
	_, _, rerr := ReadPDF(context.Background(), writeMinimalPDF("some text"), testPDFLimits)
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
	want := unusedPDFPool(t)
	initFn = func() (*pdfWorkerPool, error) {
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
	initFn = func() (*pdfWorkerPool, error) {
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

func TestReadPDFCancelledBeforeAnyWork(t *testing.T) {
	// A caller that has already given up must not pay for runtime init, nor
	// hold a worker for a result nobody will read.
	savePDFRuntime(t)
	resetPDFRuntime()
	calls := 0
	pool := unusedPDFPool(t)
	initFn = func() (*pdfWorkerPool, error) {
		calls++
		return pool, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pages, truncated, err := ReadPDF(ctx, writeMinimalPDF("never extracted"), testPDFLimits)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, pages)
	assert.False(t, truncated)
	assert.Equal(t, 0, calls, "a cancelled call must not reach the pool at all")
}

func TestReadPDFAbandonedWorkerWaitReportsCancellation(t *testing.T) {
	// The pool's only permit is taken and no worker exists, so the call can
	// only queue. A caller that gives up while queueing must be told its own
	// cause: a queue is not a document that failed.
	savePDFRuntime(t)
	resetPDFRuntime()
	pool := unusedPDFPool(t)
	<-pool.spawn
	initFn = func() (*pdfWorkerPool, error) { return pool, nil }

	// The pre-pool gate passes and so does the one the wait itself makes, so
	// the cut lands where the wait gives up - the one place where a context
	// that reports itself done without an error of its own would otherwise
	// return no content and no error at all.
	done := readPDFAsync(&errAfter{Context: cancelledContext(), limit: 2}, writeMinimalPDF("some text"), testPDFLimits)
	select {
	case got := <-done:
		assert.ErrorIs(t, got.err, context.Canceled, "a queue must not be reported as an engine that failed")
	case <-time.After(5 * time.Second):
		t.Fatal("ReadPDF kept queueing for a caller that had given up")
	}
}

// errAfter reports itself cancelled from the n-th Err() call on. readPDF
// consults ctx at fixed points on ONE goroutine, so counting them cuts an
// extraction at an exact place - where a wall-clock deadline would race the
// machine. Done() stays the embedded context's, which is the second half of
// the instrument: over a live context only the counted Err() calls see the
// cut, and over an already-cancelled one the cut lands wherever readPDF waits
// on Done() - with the count deciding how much of the call happens first.
type errAfter struct {
	context.Context
	calls int
	limit int
}

func (c *errAfter) Err() error {
	c.calls++
	if c.calls > c.limit {
		return context.Canceled
	}
	return nil
}

// cancelledContext is a context that is already done; the cut layered on top of
// it decides how far a call gets before it is allowed to see that.
func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestReadPDFCancellationAbortsRunningScan(t *testing.T) {
	// Call 1 is the pre-pool gate, call 2 the gate taken with a worker held.
	// The cut lands on that second gate: past the queue, before the job. No
	// content may reach the caller - partial content on abort would make
	// findings depend on how far the machine got.
	pool := singleWorkerPool(t)
	data := writeMinimalPDF("first page text", "second page text", "third page text")
	// A warm worker in the pool, so acquisition never consults the context and
	// the cut lands where this test says it does.
	_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pool.idle, 1)

	cut := &errAfter{Context: context.Background(), limit: 1}
	pages, truncated, extractTime, err := readPDF(cut, data, testPDFLimits)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, pages, "an aborted scan must return no content (determinism)")
	assert.False(t, truncated)
	assert.Positive(t, extractTime, "the abort must land after acquisition, not on the pre-pool gate")
	assert.Equal(t, 2, cut.calls, "the extraction must end at the FIRST checkpoint past the cut")
}

func TestReadPDFCancellationDuringExtractionDiscardsEverything(t *testing.T) {
	// The cut lands past both gates, on the wait for an answer that can never
	// arrive: the worker takes the job and stops itself, so nothing can race
	// the caller's own abort. The caller must be told its own cause, get no
	// content, and not be left waiting on a process that will never reply.
	pool := stubWorkerPool(t, stubStopMidJob)
	data := writeMinimalPDF("first page text", "second page text")
	// The stub answers its first job, which warms the pool: acquisition below
	// then takes that worker without ever consulting the context.
	_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pool.idle, 1)

	cut := &errAfter{Context: cancelledContext(), limit: 2}
	type outcome struct {
		pages       [][]byte
		truncated   bool
		extractTime time.Duration
		err         error
	}
	done := make(chan outcome, 1)
	go func() {
		pages, truncated, extractTime, err := readPDF(cut, data, testPDFLimits)
		done <- outcome{pages, truncated, extractTime, err}
	}()

	select {
	case got := <-done:
		assert.ErrorIs(t, got.err, context.Canceled)
		assert.Nil(t, got.pages, "an aborted scan must return no content (determinism)")
		assert.False(t, got.truncated)
		assert.Positive(t, got.extractTime, "the abort must land after acquisition, not on the pre-pool gate")
		assert.Equal(t, 3, cut.calls, "the extraction must end at the FIRST checkpoint past the cut")
	case <-time.After(pdfWorkerGrace + 10*time.Second):
		t.Fatal("readPDF never returned: the silent worker was not killed after the grace")
	}
}

func TestReadPDFCancelledAnswerWithinGraceKeepsTheWorker(t *testing.T) {
	// A cancelled request is routine, and an answer already on its way costs
	// nothing to wait for: the content is still discarded, but the process that
	// produced it must survive - killing a healthy worker per cancelled request
	// is how a server ends up starting one per file.
	//
	// The grace is a pool timing, and a generous one here: what is asserted is
	// which fate the worker meets, not how fast this machine can answer one job.
	timings := testPDFPoolTimings()
	timings.grace = 10 * time.Second
	pool := installPDFPool(t, newPDFWorkerPool(testExecutable(t), 1, timings))
	data := writeMinimalPDF("cancelled but answered")
	_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.NoError(t, err)
	warm := <-pool.idle
	pool.idle <- warm

	pages, truncated, err := ReadPDF(&errAfter{Context: cancelledContext(), limit: 2}, data, testPDFLimits)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, pages, "a cancelled scan reports no content, however far it got")
	assert.False(t, truncated)

	require.Len(t, pool.idle, 1, "the worker that answered inside the grace must go back to the pool")
	assert.Same(t, warm, <-pool.idle, "and it must be the same process, not a replacement")
}

func TestReadPDFConcurrentBatch(t *testing.T) {
	// Double-checked lazy init plus pool under concurrency: more goroutines
	// than the pool has workers.
	data := writeMinimalPDF("concurrent page")
	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			pages, _, err := ReadPDF(context.Background(), data, testPDFLimits)
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
