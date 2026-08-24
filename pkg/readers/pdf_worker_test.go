package readers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eawag-rdm/pc/pkg/output"
)

// pdfStubEnv selects a worker that misbehaves on purpose. The pool always
// starts the same binary with the same argv, so the only way to hand it a
// worker that lies is through the environment its children inherit - the child
// re-execution these tests need, like the lazy-init test's PC_LAZY_PDF_CHILD.
const pdfStubEnv = "PC_PDF_STUB_WORKER"

const (
	stubEngineFailure = "engine-failure" // greets, then fails every job
	stubDieMidJob     = "die-mid-job"    // greets, takes a job, dies unanswered
	stubStopMidJob    = "stop-mid-job"   // answers once, then stops itself mid-job
	stubNoHello       = "no-hello"       // never greets
	stubBadVersion    = "bad-version"    // greets in a protocol nobody speaks
)

// handlePDFStubWorker turns this process into one of the workers the real one
// never is: silent, wrong-versioned, or broken. Called from TestMain before the
// real sentinel handler, so a child started while a test asked for a stub runs
// the stub instead of the extraction loop.
func handlePDFStubWorker() {
	if len(os.Args) < 2 || os.Args[1] != pdfWorkerArg {
		return
	}
	enc := gob.NewEncoder(os.Stdout)
	switch os.Getenv(pdfStubEnv) {
	case stubEngineFailure:
		if err := enc.Encode(&pdfHello{ProtocolVersion: pdfProtocolVersion}); err != nil {
			os.Exit(1)
		}
		in := bufio.NewReader(os.Stdin)
		var buf []byte
		for {
			if _, err := readPDFJob(in, &buf); err != nil {
				os.Exit(0)
			}
			res := pdfResult{ErrCode: pdfErrRuntime, Detail: "stub engine failure"}
			if err := enc.Encode(&res); err != nil {
				os.Exit(1)
			}
		}
	case stubDieMidJob:
		if err := enc.Encode(&pdfHello{ProtocolVersion: pdfProtocolVersion}); err != nil {
			os.Exit(1)
		}
		var buf []byte
		_, _ = readPDFJob(bufio.NewReader(os.Stdin), &buf)
		os.Exit(1) // the job is read, the answer never comes
	case stubStopMidJob:
		if err := enc.Encode(&pdfHello{ProtocolVersion: pdfProtocolVersion}); err != nil {
			os.Exit(1)
		}
		in := bufio.NewReader(os.Stdin)
		var buf []byte
		for job := 0; ; job++ {
			if _, err := readPDFJob(in, &buf); err != nil {
				os.Exit(0)
			}
			if job > 0 {
				// Wedged from the second job on: nothing this worker does can
				// race the caller's own abort, and only a signal ends it. The
				// first job is answered so the pool can be warmed first.
				_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
				time.Sleep(time.Hour)
			}
			if err := enc.Encode(&pdfResult{Pages: [][]byte{[]byte("stub page")}}); err != nil {
				os.Exit(1)
			}
		}
	case stubNoHello:
		// Sleeping, not blocking on nothing: a Go process whose goroutines are
		// all parked dies of its own deadlock detector, which would hand the
		// parent an EOF instead of the silence this stub exists to produce.
		time.Sleep(time.Hour)
	case stubBadVersion:
		_ = enc.Encode(&pdfHello{ProtocolVersion: pdfProtocolVersion + 1})
		time.Sleep(time.Hour)
	}
}

// testPDFPoolTimings are the shipped clocks with the handshake shortened: a
// test that hands the pool a worker which never greets must not wait 30 s to
// find out. Timings are pool fields, so nothing here writes state the janitor
// goroutine reads.
func testPDFPoolTimings() pdfPoolTimings {
	timings := defaultPDFPoolTimings()
	timings.hello = 5 * time.Second
	return timings
}

// installPDFPool makes the given pool the shared runtime for one test and kills
// its workers afterwards, whatever they were left doing.
func installPDFPool(t *testing.T, pool *pdfWorkerPool) *pdfWorkerPool {
	t.Helper()
	savePDFRuntime(t)
	resetPDFRuntime()
	t.Cleanup(pool.Close)
	initFn = func() (*pdfWorkerPool, error) { return pool, nil }
	return pool
}

// singleWorkerPool installs a pool of exactly one worker, so the tests below
// know which process serves the next job.
func singleWorkerPool(t *testing.T) *pdfWorkerPool {
	t.Helper()
	return installPDFPool(t, newPDFWorkerPool(testExecutable(t), 1, testPDFPoolTimings()))
}

// stubWorkerPool is singleWorkerPool with the misbehaving worker of the given
// kind. The environment is set before any worker starts, so every child of this
// pool is the stub.
func stubWorkerPool(t *testing.T, kind string) *pdfWorkerPool {
	t.Helper()
	t.Setenv(pdfStubEnv, kind)
	return singleWorkerPool(t)
}

func testExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	return exe
}

func TestPDFWorkerDyingMidJobIsARuntimeError(t *testing.T) {
	// A worker that takes a job and dies with it leaves nothing to read. That
	// is the engine failing, not the document, and the corpse must not stay in
	// the pool for the next file to trip over.
	pool := stubWorkerPool(t, stubDieMidJob)

	pages, truncated, err := ReadPDF(context.Background(), writeMinimalPDF("lost page"), testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFRuntime, "a worker that died mid-job is an engine failure, not a parse failure")
	assert.Nil(t, pages)
	assert.False(t, truncated)

	assert.Empty(t, pool.idle, "a worker that died mid-job must not be handed to the next file")
	assert.Len(t, pool.spawn, 1, "its permit must come back so the pool can start a replacement")
}

func TestPDFWorkerDeadWhileIdleIsReplaced(t *testing.T) {
	// A worker can die between two jobs (OOM killer, an operator, its own
	// runtime): the pool must notice before dispatching to it, or the next file
	// pays for a corpse with a bogus engine error.
	pool := singleWorkerPool(t)
	data := writeMinimalPDF("worker crash page")

	pages, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pages, 1)

	w := <-pool.idle
	require.NoError(t, w.cmd.Process.Kill())
	<-w.exited
	pool.idle <- w

	pages, _, err = ReadPDF(context.Background(), data, testPDFLimits)
	assert.NoError(t, err, "a worker that died while idle must be replaced, not dispatched to")
	assert.Len(t, pages, 1)
}

func TestPDFWorkerReportingEngineFailureIsDiscarded(t *testing.T) {
	// An engine that broke after its greeting answers every job the same way,
	// so the worker that reported it must not be handed the next document.
	pool := stubWorkerPool(t, stubEngineFailure)
	// The one warning a discarded worker leaves is the only place a failure
	// inside a child process is ever visible, so it is captured, not printed.
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()
	t.Cleanup(func() {
		output.GlobalLogger.ClearMessages()
		output.GlobalLogger.SetJSONMode(false)
	})

	pages, _, err := ReadPDF(context.Background(), writeMinimalPDF("engine failure page"), testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFRuntime)
	assert.ErrorContains(t, err, "stub engine failure", "the worker's own cause must reach the caller")
	assert.Nil(t, pages)

	assert.Empty(t, pool.idle, "a worker that reported a broken engine must not go back into the pool")
	assert.Len(t, pool.spawn, 1, "its permit must come back, or the pool shrinks on every failure")

	msgs := output.GlobalLogger.Drain()
	require.Len(t, msgs, 1, "a discarded worker must be reported exactly once")
	assert.Contains(t, msgs[0].Message, "PDF worker")
}

func TestPDFWorkerRetiredWhenIdleTooLong(t *testing.T) {
	// A pool that went quiet must let its processes go on its own: nothing else
	// looks at them until the next PDF, which may never come.
	timings := testPDFPoolTimings()
	timings.idle = 10 * time.Millisecond
	timings.sweep = 10 * time.Millisecond
	pool := installPDFPool(t, newPDFWorkerPool(testExecutable(t), 1, timings))

	pages, _, err := ReadPDF(context.Background(), writeMinimalPDF("idle page"), testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	require.Len(t, pool.idle, 1)

	// Nothing below touches the pool: the sweep has to happen by itself. The
	// permit is what completes it, so waiting for that also waits for the kill.
	deadline := time.Now().Add(5 * time.Second)
	for len(pool.spawn) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Empty(t, pool.idle, "an idle worker past its timeout must be killed without being asked for")
	assert.Len(t, pool.spawn, 1, "and its permit handed back, or the pool never recovers its size")
}

func TestPDFWorkerWithoutHelloIsRefusedAndSuppressed(t *testing.T) {
	// A worker that never greets is one still compiling, or hung: it must not
	// be handed a job, and the pool must not start another one straight away -
	// every PDF of the run would otherwise pay the same handshake timeout to
	// learn the same thing.
	t.Setenv(pdfStubEnv, stubNoHello)
	timings := testPDFPoolTimings()
	timings.hello = 300 * time.Millisecond
	timings.spawnBackoff = 10 * time.Second
	pool := installPDFPool(t, newPDFWorkerPool(testExecutable(t), 1, timings))

	data := writeMinimalPDF("never extracted")
	start := time.Now()
	_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFRuntime)
	assert.GreaterOrEqual(t, time.Since(start), timings.hello, "the handshake must be waited out once")
	assert.Less(t, time.Since(start), 5*time.Second, "and only once - the deadline must end it")
	assert.Len(t, pool.spawn, 1, "a worker that never started must give its permit back")

	// Inside the backoff window: the same cause, without starting anything.
	start = time.Now()
	_, _, again := ReadPDF(context.Background(), data, testPDFLimits)
	assert.ErrorIs(t, again, ErrPDFRuntime)
	assert.Less(t, time.Since(start), timings.hello, "a suppressed start must not repeat the handshake wait")
}

func TestPDFWorkerColdStartFailureIsSharedWithQueuedCallers(t *testing.T) {
	// Only one worker may start until the first has greeted, so a cold start
	// that fails leaves the others queued behind it. They must be told, not
	// left to serialize their own handshake timeouts behind the same failure.
	t.Setenv(pdfStubEnv, stubNoHello)
	timings := testPDFPoolTimings()
	timings.hello = 500 * time.Millisecond
	timings.spawnBackoff = 10 * time.Second
	installPDFPool(t, newPDFWorkerPool(testExecutable(t), 4, timings))

	data := writeMinimalPDF("never extracted")
	const callers = 4
	done := make(chan error, callers)
	start := time.Now()
	for i := 0; i < callers; i++ {
		go func() {
			_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
			done <- err
		}()
	}
	for i := 0; i < callers; i++ {
		select {
		case err := <-done:
			assert.ErrorIs(t, err, ErrPDFRuntime)
		case <-time.After(10 * time.Second):
			t.Fatal("a caller queued behind a failed cold start never returned")
		}
	}
	assert.Less(t, time.Since(start), callers*timings.hello,
		"queued callers must share the cold start's outcome, not each wait for their own")
}

func TestPDFWorkerStartAbandonedByItsCallerIsNotABackoff(t *testing.T) {
	// A caller that gives up while a worker is still greeting must be told so
	// at once, not at the end of the handshake it stopped waiting for. And the
	// machine is fine: an abandoned start says nothing about whether workers can
	// be started, so it must not be counted against the next PDF.
	t.Setenv(pdfStubEnv, stubNoHello)
	timings := testPDFPoolTimings()
	timings.hello = 5 * time.Second
	timings.spawnBackoff = time.Hour
	pool := installPDFPool(t, newPDFWorkerPool(testExecutable(t), 1, timings))

	data := writeMinimalPDF("never extracted")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, _, err := ReadPDF(ctx, data, testPDFLimits)
	assert.ErrorIs(t, err, context.Canceled, "the caller's own cause, not the handshake's")
	assert.Less(t, time.Since(start), timings.hello, "the cancellation must end the wait, not its deadline")
	assert.Len(t, pool.spawn, 1, "the permit must come back for the next caller")

	pool.mu.Lock()
	memoized := pool.spawnErr
	pool.mu.Unlock()
	assert.NoError(t, memoized, "an abandoned start must not arm the spawn backoff")

	// What that is worth: the next caller still gets a worker, where a memoized
	// failure would refuse it for the whole backoff window.
	t.Setenv(pdfStubEnv, stubStopMidJob) // greets, answers its first job
	pages, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	assert.NoError(t, err, "a start the pool never refused must not be replayed as one")
	assert.Len(t, pages, 1)
}

func TestPDFWorkerSpawnRefusalEndsWithItsWindow(t *testing.T) {
	// The memoized start failure is the answer only while the pool refuses to
	// try again. Past the window it must not be served to a caller woken by the
	// cold start, or that caller is refused for a start the pool would now make.
	t.Setenv(pdfStubEnv, stubNoHello)
	timings := testPDFPoolTimings()
	timings.hello = 100 * time.Millisecond
	timings.spawnBackoff = 100 * time.Millisecond
	pool := installPDFPool(t, newPDFWorkerPool(testExecutable(t), 1, timings))

	data := writeMinimalPDF("never extracted")
	_, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.ErrorIs(t, err, ErrPDFRuntime)
	assert.Error(t, pool.lastSpawnError(), "inside the window the failure is what queued callers are told")

	pool.mu.Lock()
	window := pool.noSpawnBefore
	pool.mu.Unlock()
	time.Sleep(time.Until(window) + 10*time.Millisecond)
	assert.NoError(t, pool.lastSpawnError(), "past the window there is nothing left to refuse with")

	// And a worker that greets now is served, not answered with what the last
	// one did.
	t.Setenv(pdfStubEnv, stubStopMidJob) // greets, answers its first job
	pages, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	assert.NoError(t, err, "a start refused only by a window that has passed must be retried")
	assert.Len(t, pages, 1)
}

func TestPDFWorkerProtocolMismatchIsRefused(t *testing.T) {
	// The binary can be replaced under a running process; a worker started
	// from the new one must be refused rather than spoken to in a dialect this
	// parent does not have.
	stubWorkerPool(t, stubBadVersion)

	_, _, err := ReadPDF(context.Background(), writeMinimalPDF("never extracted"), testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFRuntime)
	assert.ErrorContains(t, err, "protocol", "the refusal must name its cause")
}

func TestPDFWorkerDeadlineKillsWedgedWorker(t *testing.T) {
	pool := singleWorkerPool(t)
	data := writeMinimalPDF("wedged worker page")

	pages, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pages, 1)

	// SIGSTOP stands in for a worker wedged inside wasm: it answers nothing and
	// never reaches its own deadline, so only the parent's SIGKILL gets the
	// call back - which is what the grace past the job timeout is for.
	w := <-pool.idle
	require.NoError(t, w.cmd.Process.Signal(syscall.SIGSTOP))
	pool.idle <- w

	limits := testPDFLimits
	limits.Timeout = 50 * time.Millisecond
	done := readPDFAsync(context.Background(), data, limits)
	select {
	case got := <-done:
		assert.ErrorIs(t, got.err, ErrPDFTimeout)
		assert.Nil(t, got.pages, "a killed extraction must return no partial content (determinism)")
		assert.False(t, got.truncated)
	case <-time.After(limits.Timeout + pdfWorkerGrace + 5*time.Second):
		t.Fatal("ReadPDF never returned: the wedged worker was not killed")
	}

	pages, _, err = ReadPDF(context.Background(), data, testPDFLimits)
	assert.NoError(t, err)
	assert.Len(t, pages, 1)
}

// outsizedPDF is a valid document just past pdfRecycleInputBytes, built from
// pages of the near-gate benchmark's shape. The recycle gate reads the
// document's own length, so the fixture has to carry the bytes.
func outsizedPDF() []byte {
	const pageText = 24 * 1024
	pages := make([]string, 0, pdfRecycleInputBytes/pageText+1)
	for size := 0; size < pdfRecycleInputBytes; size += pageText {
		pages = append(pages, strings.Repeat("outsized filler words ", pageText/22))
	}
	return writeMinimalPDF(pages...)
}

func TestPDFWorkerRetiredAfterOutsizedDocument(t *testing.T) {
	// A worker keeps its high-water mark - wasm memory, job buffer, extracted
	// text - for the life of the process, so one that handled a big document is
	// replaced rather than kept warm.
	pool := singleWorkerPool(t)
	limits := testPDFLimits
	limits.MaxFileBytes = 8 << 20 // the recycle gate, not the admission gate, is the subject

	// A warm pool first, so the process that reads the outsized document below
	// is one this test holds and can watch afterwards.
	pages, _, err := ReadPDF(context.Background(), writeMinimalPDF("recycle page"), limits)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	w := <-pool.idle
	pool.idle <- w

	big := outsizedPDF()
	require.Greater(t, len(big), pdfRecycleInputBytes, "the fixture must cross the recycle gate")
	pages, _, err = ReadPDF(context.Background(), big, limits)
	require.NoError(t, err, "an outsized document is still extracted in full; only the worker is replaced")
	require.NotEmpty(t, pages)

	select {
	case <-w.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker that read an outsized document was left running")
	}
	assert.Empty(t, pool.idle, "the worker that read an outsized document must not be kept")
	assert.Len(t, pool.spawn, 1, "its permit must come back so the pool can start a fresh one")

	// The extracted text is the other way a worker grows past what it should
	// keep: a dense document is small on the wire in, large on the wire out.
	pages, _, err = ReadPDF(context.Background(), writeMinimalPDF("recycle page"), testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	w = <-pool.idle
	dense := pdfExchange{res: pdfResult{Pages: [][]byte{make([]byte, pdfRecycleTextBytes+1)}}}
	pool.handBack(w, dense, 0)
	assert.Empty(t, pool.idle, "the worker that extracted an outsized text must not be kept either")
	assert.Len(t, pool.spawn, 1)
}

func TestSpawnPDFWorkerRefusesWithoutAHandover(t *testing.T) {
	// Both refusals come from what the sentinel handover recorded: a binary
	// that never installed it would re-run its own program as a "worker", and a
	// worker starting workers would fork a tree of them per PDF.
	exe := testExecutable(t)
	for _, test := range []struct {
		name  string
		flag  *atomic.Bool
		value bool
		want  string
	}{
		{"sentinel never installed", &pdfWorkerSentinelInstalled, false, "does not dispatch"},
		{"already a worker", &pdfWorkerSelf, true, "refusing to start"},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved := test.flag.Load()
			test.flag.Store(test.value)
			t.Cleanup(func() { test.flag.Store(saved) })

			w, err := spawnPDFWorker(context.Background(), exe, time.Second)
			assert.Nil(t, w)
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestPDFWorkerSpawnFailureReturnsItsPermit(t *testing.T) {
	// A binary that cannot be started is an engine failure, not a parse
	// failure, and it must leave the pool able to try again.
	savePDFRuntime(t)
	resetPDFRuntime()
	pool := installPDFPool(t, newPDFWorkerPool(filepath.Join(t.TempDir(), "no-such-binary"), 1, testPDFPoolTimings()))

	_, _, err := ReadPDF(context.Background(), writeMinimalPDF("never extracted"), testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFRuntime)
	assert.Len(t, pool.spawn, 1, "a failed start must hand its permit back")
}

func TestReadPDFReportsBothCausesWhenCancelledAroundAFailedStart(t *testing.T) {
	// A pool that failed for its own reasons around a cancellation would
	// otherwise be invisible: the caller sees only "cancelled" and the engine
	// failure that also happened goes unreported.
	pool := installPDFPool(t, newPDFWorkerPool(filepath.Join(t.TempDir(), "no-such-binary"), 1, testPDFPoolTimings()))

	// The pre-pool gate passes; the context ends while the start is failing, so
	// both causes exist at the same moment.
	_, _, err := ReadPDF(&errAfter{Context: context.Background(), limit: 1}, writeMinimalPDF("never extracted"), testPDFLimits)
	assert.ErrorIs(t, err, context.Canceled, "the caller's own cause outranks the engine's")
	assert.ErrorContains(t, err, "worker:", "the failed start must stay visible beside it")
	assert.Len(t, pool.spawn, 1)
}

func TestClosedPDFWorkerPoolRefusesJobs(t *testing.T) {
	// Shutdown is one-way: the drained pool must not quietly start the
	// processes the shutdown just ended.
	pool := singleWorkerPool(t)
	data := writeMinimalPDF("closed pool page")
	pages, _, err := ReadPDF(context.Background(), data, testPDFLimits)
	require.NoError(t, err)
	require.Len(t, pages, 1)
	w := <-pool.idle
	pool.idle <- w

	ClosePDFWorkers()
	select {
	case <-w.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("Close left an idle worker running")
	}

	ClosePDFWorkers() // second close must be safe

	_, _, err = ReadPDF(context.Background(), data, testPDFLimits)
	assert.ErrorIs(t, err, ErrPDFRuntime, "a job after the shutdown must be refused, not served by a new process")
	assert.Empty(t, pool.idle, "a closed pool must start nothing")
}

func TestPDFWorkerReleasedIntoAClosedPoolIsKilled(t *testing.T) {
	// Close drains the idle channel only: a worker out on a job when the
	// shutdown runs comes back through release afterwards, and re-idling it
	// would hand the next caller a process the shutdown already disowned.
	pool := singleWorkerPool(t)
	w, err := pool.acquire(context.Background())
	require.NoError(t, err)

	pool.Close()
	pool.release(w)

	select {
	case <-w.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("a worker released after Close was left running")
	}
	assert.Empty(t, pool.idle, "a worker released after Close must be killed, not put back to work")
}

func TestSweepStaleCacheTemps(t *testing.T) {
	// wazero renames a compiled module into place, so a worker SIGKILLed
	// mid-compile strands the temp file it was writing and nothing else ever
	// removes it. A compile still running in another process must survive.
	root := t.TempDir()
	version := filepath.Join(root, "wazero-v1.2.3-amd64-linux")
	require.NoError(t, os.MkdirAll(version, 0o755))

	write := func(name string, age time.Duration) string {
		path := filepath.Join(version, name)
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
		when := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, when, when))
		return path
	}
	stale := write("module.123.tmp", 2*time.Hour)
	fresh := write("module.456.tmp", time.Minute)
	cached := write("0102030405060708", 2*time.Hour)

	sweepStaleCacheTemps(root)

	assert.NoFileExists(t, stale, "an abandoned temp file must be removed")
	assert.FileExists(t, fresh, "a temp file this young may still be a compile in flight")
	assert.FileExists(t, cached, "a cache entry is not a temp file")
}

func TestPDFResultRoundTripsEveryErrorCode(t *testing.T) {
	// The wire is the only thing between the extraction and the caller's
	// errors.Is: a code that lost its sentinel would turn a password-protected
	// document into a parse failure, and a detail that did not travel would
	// drop the only cause an engine failure ever reports.
	for _, test := range []struct {
		name     string
		code     uint8
		detail   string
		sentinel error
		want     string
	}{
		{"ok", pdfErrNone, "", nil, ""},
		{"password", pdfErrPassword, "", ErrPDFPassword, "pdf is password-protected"},
		{"timeout", pdfErrTimeout, "", ErrPDFTimeout, "pdf extraction timed out"},
		{"too many pages", pdfErrTooManyPages, "7 pages", ErrPDFTooManyPages, "pdf has too many pages: 7 pages"},
		{"runtime", pdfErrRuntime, "wasm init failed", ErrPDFRuntime, "pdf engine unavailable: wasm init failed"},
		{"other", pdfErrOther, "could not open document", nil, "could not open document"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sent := pdfResult{
				Pages:        [][]byte{nil, []byte("page two")},
				Truncated:    true,
				ExtractNanos: 42,
				ErrCode:      test.code,
				Detail:       test.detail,
			}
			var buf bytes.Buffer
			require.NoError(t, gob.NewEncoder(&buf).Encode(&sent))
			var got pdfResult
			require.NoError(t, gob.NewDecoder(&buf).Decode(&got))

			assert.Len(t, got.Pages, 2, "empty page placeholders must survive the wire")
			assert.Equal(t, "page two", string(got.Pages[1]))
			assert.True(t, got.Truncated)
			assert.Equal(t, int64(42), got.ExtractNanos)

			err := pdfErrFromWire(got.ErrCode, got.Detail)
			if test.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, test.want, "the cause must arrive as the worker wrote it")
			if test.sentinel != nil {
				assert.ErrorIs(t, err, test.sentinel, "a sentinel must stay matchable across the wire")
			}
		})
	}
}
