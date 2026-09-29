package readers

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eawag-rdm/pc/pkg/output"
)

// PDFLimits bounds PDF text extraction. Like ArchiveLimits it fails closed:
// non-positive limits mean nothing is extracted.
//
// MaxFileBytes and MaxPages are ADMISSION gates, not truncation points: a
// document over either is skipped whole, with an acknowledgement, so a
// report never mixes "fully scanned" with "scanned as far as we got". Both
// are checked before any page is touched (the page count costs ~1% of an
// extraction), which is what keeps a long document cheap to reject.
// MaxTextBytes does truncate - it guards dense-text amplification within an
// already-admitted document - and Timeout is the backstop against
// pathological files (kept internal so results stay deterministic).
type PDFLimits struct {
	MaxFileBytes int64
	MaxPages     int
	MaxTextBytes int64
	Timeout      time.Duration
}

// DefaultPDFTimeout backstops a single file's extraction; deliberately a
// constant, not config - a knob would tune nondeterminism into the scan.
const DefaultPDFTimeout = 30 * time.Second

// maxArchivePDFTime bounds cumulative PDF extraction wall-time per archive
// iterator: without it a crafted archive of many pathological members
// amplifies the per-member timeout to hours inside one check item. Constant,
// not config, same determinism stance as DefaultPDFTimeout. Covers ~6000
// median-cost or ~50 heavy legitimate PDFs per archive (benchmarked).
const maxArchivePDFTime = 4 * DefaultPDFTimeout

// pdfWorkerGrace is the slack the parent gives a worker on both ends of a job:
// past the job's own timeout before it is killed (the worker ends an
// overrunning document itself, so this is the backstop for one wedged inside
// wasm), and past a cancellation before an answer already on its way is given
// up on. Routine cancellation is common, and its cost must not be a process.
const pdfWorkerGrace = time.Second

// pdfHelloTimeout bounds the wait for a new worker's greeting. Generous
// because the first worker on a machine compiles the wasm module, which costs
// seconds where a warm start costs milliseconds.
const pdfHelloTimeout = 30 * time.Second

// pdfWorkerIdleTimeout retires a worker unused for this long, and
// pdfIdleSweepEvery is how often that is checked - so a worker outlives its
// last job by between one and two of these. A scan that is over must not leave
// four wasm runtimes resident for the life of a server.
const (
	pdfWorkerIdleTimeout = 60 * time.Second
	pdfIdleSweepEvery    = 60 * time.Second
)

// pdfSpawnBackoff is the first window in which a pool that could not start a
// worker refuses to try again, doubling per consecutive failure up to
// pdfSpawnBackoffMax. It suppresses STARTING processes, never extraction: the
// workers a pool already has keep serving throughout.
const (
	pdfSpawnBackoff    = 60 * time.Second
	pdfSpawnBackoffMax = time.Hour
)

// pdfRecycleInputBytes / pdfRecycleTextBytes retire a worker after an outsized
// document: its wasm memory and its own heap stay at the high-water mark for
// the life of the process, where a warm respawn costs ~57 ms.
const (
	pdfRecycleInputBytes = 4 << 20
	pdfRecycleTextBytes  = 16 << 20
)

// pdfWorkerLogBytes bounds the tail of worker stderr the parent keeps per
// worker, so a chatty child cannot grow the parent's heap.
const pdfWorkerLogBytes = 8 << 10

// PDFMagic / PDFMagicWindow: PDFium accepts the "%PDF" magic at any offset
// up to 1024, so 1028 bytes is the exact window - verified against the
// engine at both boundaries (offset 1024 parses, 1025 does not). Shared so
// the file-level and member-level sniffs cannot drift apart.
var PDFMagic = []byte("%PDF")

const PDFMagicWindow = 1028

// pdfWasmMemoryLimitPages caps each sandbox instance at 1 GiB (64 KiB pages).
// Document bytes are copied into wasm memory, so the input gate below keeps
// admissible documents at half this ceiling, leaving the other half for
// pdfium's working set.
const pdfWasmMemoryLimitPages = 16384

// MaxPDFInputBytes rejects documents that cannot fit the wasm sandbox
// alongside pdfium's working set (512 MiB, half the memory ceiling).
// Callers with the size at hand can gate before reading the file at all.
const MaxPDFInputBytes = pdfWasmMemoryLimitPages * 65536 / 2

var (
	// ErrPDFPassword marks password-protected documents (distinct ack).
	ErrPDFPassword = errors.New("pdf is password-protected")
	// ErrPDFTimeout marks extraction hitting the wall-time backstop. No
	// partial content is returned - partial-on-timeout would make findings
	// depend on machine speed.
	ErrPDFTimeout = errors.New("pdf extraction timed out")
	// ErrPDFTooLarge marks documents rejected by the size gate before any
	// extraction work (distinct ack: "parse error" would mislead).
	ErrPDFTooLarge = errors.New("pdf too large")
	// ErrPDFTooManyPages marks documents past the page ceiling. Nothing is
	// extracted: a page cap that truncated would report a long document as
	// scanned when most of it never was.
	ErrPDFTooManyPages = errors.New("pdf has too many pages")
	// ErrPDFRuntime marks an engine that could not serve the document: a pool
	// that failed to initialize, a worker that could not be started, greeted or
	// sent the whole job, or that answered with something unreadable, or a pool
	// already shut down. Without a distinct sentinel each of those would report
	// a bogus parse error instead of the one real cause, which repeated failures
	// make every file's cause.
	ErrPDFRuntime = errors.New("pdf engine unavailable")
	// ErrPDFWorkerCrashed marks a worker that died after the whole job was
	// written to it - a small job landing in the pipe buffer counts. The
	// document most likely caused it, so, like a timeout, it is a verdict on
	// the document.
	ErrPDFWorkerCrashed = errors.New("pdf worker crashed")
)

// errPDFCancelled stands in when a context reports itself done without an
// error of its own: an abandoned extraction must never return as content-free
// success, and callers match the cancellation rather than this text.
var errPDFCancelled = fmt.Errorf("%w: pdf extraction abandoned", context.Canceled)

// errPDFPoolClosed ends jobs that arrive after the shutdown drained the pool.
var errPDFPoolClosed = errors.New("pdf worker pool is closed")

// pdfInitCooldown is the delay before a failed runtime init is retried; it
// doubles per consecutive failure up to pdfInitCooldownMax. A failure is
// memoized for that window so every later PDF gets the same error (skip ack)
// instead of a per-file init retry storm, while a transient cause (resource
// pressure) no longer disables PDF scanning until the process restarts.
// The one init failure this layer still sees is os.Executable failing - the
// processes start lazily, under the pool's own spawn backoff, which is the live
// retry policy - so it is kept as cheap insurance, not as the busy path.
// A var so tests can shrink it.
var pdfInitCooldown = 60 * time.Second

const pdfInitCooldownMax = time.Hour

// pdfRuntime is the lazily-initialized shared worker pool. A run without PDFs
// never pays for it.
var pdfRuntime struct {
	ready       atomic.Pointer[pdfWorkerPool] // fast path: no mutex once initialized
	mu          sync.Mutex
	pool        *pdfWorkerPool
	err         error
	lastAttempt time.Time
	cooldown    time.Duration
	initialized atomic.Bool
}

// initFn builds the pool; a var so tests can inject init failures.
var initFn = defaultInit

// pdfPool is on the hot path (once per PDF, from GOMAXPROCS workers), so the
// initialized case must stay lock-free: only the retry path takes the mutex.
func pdfPool() (*pdfWorkerPool, error) {
	if p := pdfRuntime.ready.Load(); p != nil {
		return p, nil
	}
	return pdfPoolSlow()
}

func pdfPoolSlow() (*pdfWorkerPool, error) {
	pdfRuntime.mu.Lock()
	defer pdfRuntime.mu.Unlock()

	// Another goroutine may have won the race while we waited.
	if pdfRuntime.err == nil && pdfRuntime.pool != nil {
		return pdfRuntime.pool, nil
	}
	if pdfRuntime.err != nil && time.Since(pdfRuntime.lastAttempt) < pdfRuntime.cooldown {
		return nil, pdfRuntime.err
	}

	pool, err := initFn()
	// Stamped AFTER the attempt: a slow init must not burn its own cooldown
	// (a failure taking longer than the window would be retried immediately).
	pdfRuntime.lastAttempt = time.Now()
	pdfRuntime.initialized.Store(true)
	if err == nil && pool == nil {
		err = errors.New("pdf runtime returned no pool")
	}
	if err != nil {
		pdfRuntime.err = err
		pdfRuntime.cooldown = min(max(2*pdfRuntime.cooldown, pdfInitCooldown), pdfInitCooldownMax)
		return nil, err
	}
	// Clear the memoized failure and the backoff: a retry that succeeded must
	// not keep serving the stale error, nor mis-arm a future re-init.
	pdfRuntime.err = nil
	pdfRuntime.cooldown = 0
	pdfRuntime.pool = pool
	pdfRuntime.ready.Store(pool)
	return pool, nil
}

// defaultInit resolves the worker binary and builds the pool around it. The
// pool itself cannot fail - the processes it manages start lazily, on the first
// PDF - so the one init failure left is a binary that cannot name itself.
func defaultInit() (*pdfWorkerPool, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("pdf worker binary: %w", err)
	}
	// Small fixed pool, NOT one worker per check worker: check workers block on
	// acquisition as backpressure, and each live worker costs a wasm runtime.
	// This size is a ONE-SHOT SNAPSHOT taken at the first PDF and kept for the
	// process lifetime, where the check pools re-read the budget per pass -
	// sound only because the budget is set once at startup and never moves
	// afterwards.
	return newPDFWorkerPool(exe, min(4, runtime.GOMAXPROCS(0)), defaultPDFPoolTimings()), nil
}

// pdfPoolTimings are the pool's clocks. Fields rather than package variables
// because the janitor goroutine reads them: a test that shortened a global
// would be writing state another goroutine is reading.
type pdfPoolTimings struct {
	hello        time.Duration // how long a new worker may take to greet
	idle         time.Duration // how long an unused worker is kept
	sweep        time.Duration // how often idle workers are looked at
	spawnBackoff time.Duration // first refusal window after a failed start
	grace        time.Duration // slack on both ends of a job
}

func defaultPDFPoolTimings() pdfPoolTimings {
	return pdfPoolTimings{
		hello:        pdfHelloTimeout,
		idle:         pdfWorkerIdleTimeout,
		sweep:        pdfIdleSweepEvery,
		spawnBackoff: pdfSpawnBackoff,
		grace:        pdfWorkerGrace,
	}
}

// pdfWorkerPool hands out extraction subprocesses. Two channels, not one: idle
// holds started workers, spawn holds the permits for those not started yet, and
// a job needs one or the other - which caps live workers, and concurrent
// extractions, at the pool size. Workers start lazily, so a run without PDFs
// starts none, and an idle worker is always preferred over a permit, so a
// sequential scan reuses one process instead of starting the whole pool.
type pdfWorkerPool struct {
	exe     string
	size    int
	timings pdfPoolTimings
	idle    chan *pdfWorker
	spawn   chan struct{}
	// warmed and coldDone are the cold start: until one worker has greeted the
	// parent only that one may start, because the first pays for compiling the
	// wasm module and four processes doing it at once cost four times the
	// wall-clock. coldDone publishes how that first attempt went, so callers
	// queued behind it hear a failure instead of each paying a handshake
	// timeout to rediscover it.
	warmed   atomic.Bool
	coldDone chan struct{}
	coldOnce sync.Once
	// done is closed by Close, which is what releases callers already queued.
	done      chan struct{}
	closeOnce sync.Once

	// mu guards the fields below and serializes Close against the janitor.
	mu            sync.Mutex
	closed        bool
	spawnErr      error
	spawnCooldown time.Duration
	noSpawnBefore time.Time
}

func newPDFWorkerPool(exe string, size int, timings pdfPoolTimings) *pdfWorkerPool {
	p := &pdfWorkerPool{
		exe:      exe,
		size:     size,
		timings:  timings,
		idle:     make(chan *pdfWorker, size),
		spawn:    make(chan struct{}, size),
		coldDone: make(chan struct{}),
		done:     make(chan struct{}),
	}
	p.spawn <- struct{}{} // the cold-start permit; the rest follow the first hello
	go p.janitor()
	return p
}

// acquire blocks until a worker is free, the pool may start one, or the caller
// gives up. The wait is backpressure, not extraction time - a queue must not
// surface as a spurious "timed out" ack - but a caller that has given up, and
// one whose pool has shut down, must stop queueing.
func (p *pdfWorkerPool) acquire(ctx context.Context) (*pdfWorker, error) {
	for {
		if p.isClosed() {
			return nil, errPDFPoolClosed
		}
		// Started workers first, and only then the choice between waiting and
		// starting one: a single select would pick either at random and grow
		// the pool to its full size under a workload that never needed two.
		select {
		case w := <-p.idle:
			if p.stale(w, time.Now()) {
				p.retire(w)
				continue
			}
			return w, nil
		case <-p.done:
			return nil, errPDFPoolClosed
		default:
		}
		select {
		case w := <-p.idle:
			if p.stale(w, time.Now()) {
				p.retire(w)
				continue
			}
			return w, nil
		case <-p.spawn:
			w, err := p.startWorker(ctx)
			if err != nil {
				p.spawn <- struct{}{} // the permit outlives the failed start
				return nil, err
			}
			return w, nil
		case <-p.coldStart():
			// The pool's first worker settled it for everyone queued behind it:
			// a cold start that failed fails them all, and none of them should
			// spend another handshake timeout hearing the same thing.
			if err := p.lastSpawnError(); err != nil {
				return nil, err
			}
			continue
		case <-p.done:
			return nil, errPDFPoolClosed
		case <-ctx.Done():
			return nil, ctxCause(ctx)
		}
	}
}

// coldStart is the channel that closes when the pool's first start attempt has
// finished, or nil once one has succeeded - a nil channel never fires, so the
// arm above costs nothing for the rest of the pool's life.
func (p *pdfWorkerPool) coldStart() <-chan struct{} {
	if p.warmed.Load() {
		return nil
	}
	return p.coldDone
}

// startWorker starts one worker under the pool's spawn policy: no attempt
// inside the backoff window, none at all after Close, and the outcome recorded
// for the callers waiting on the cold start. The caller's context bounds the
// start itself, so a scan that ends does not wait out a handshake.
func (p *pdfWorkerPool) startWorker(ctx context.Context) (*pdfWorker, error) {
	p.mu.Lock()
	switch {
	case p.closed:
		p.mu.Unlock()
		return nil, errPDFPoolClosed
	case p.spawnErr != nil && time.Now().Before(p.noSpawnBefore):
		err := p.spawnErr
		p.mu.Unlock()
		return nil, err
	}
	p.mu.Unlock()

	w, err := spawnPDFWorker(ctx, p.exe, p.timings.hello)

	p.mu.Lock()
	switch {
	case err == nil:
		p.spawnErr, p.spawnCooldown, p.noSpawnBefore = nil, 0, time.Time{}
	case ctx.Err() == nil:
		// Doubling per consecutive failure: a machine that cannot start a
		// worker at all must not be asked once per PDF, and one that recovers
		// must not stay locked out for an hour. A start the CALLER ended says
		// nothing about the machine, so it is counted against neither.
		p.spawnErr = err
		p.spawnCooldown = min(max(2*p.spawnCooldown, p.timings.spawnBackoff), pdfSpawnBackoffMax)
		p.noSpawnBefore = time.Now().Add(p.spawnCooldown)
	}
	p.mu.Unlock()

	if err == nil {
		// Before the publication below: a waiter woken by it must find the pool
		// already warm, or it would wake on every acquire.
		p.warm()
	}
	p.coldOnce.Do(func() { close(p.coldDone) })
	return w, err
}

// lastSpawnError is what a caller woken by the cold start is told: the memoized
// failure while the pool still refuses to try again, and nothing once that
// window has passed - serving it afterwards would refuse a caller the pool is
// free to start a worker for.
func (p *pdfWorkerPool) lastSpawnError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.noSpawnBefore) {
		return p.spawnErr
	}
	return nil
}

func (p *pdfWorkerPool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// warm releases the permits held back for the cold start, once one worker has
// proved it can start and left a warm compilation cache behind.
func (p *pdfWorkerPool) warm() {
	if p.warmed.CompareAndSwap(false, true) {
		for i := 0; i < p.size-1; i++ {
			p.spawn <- struct{}{}
		}
	}
}

// release hands a worker back for the next job and starts its idle clock. The
// lock is held across the handover so a worker cannot land in a pool that
// Close has already drained; the send itself never blocks, because live workers
// and outstanding permits together never exceed the pool size.
func (p *pdfWorkerPool) release(w *pdfWorker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		w.kill()
		return
	}
	w.idleSince = time.Now()
	p.idle <- w
}

// retire kills a healthy worker and frees its permit: one that sat idle, one
// whose document left it holding more memory than it should keep, or one whose
// answer nobody is waiting for any more. Silent - nothing failed.
func (p *pdfWorkerPool) retire(w *pdfWorker) {
	w.kill()
	p.spawn <- struct{}{}
}

// discard retires a worker that FAILED and says so once, with its exit status
// and the tail of its stderr: otherwise a wasm compile failure, an OOM kill or
// a race report inside the child would leave no trace anywhere.
func (p *pdfWorkerPool) discard(w *pdfWorker, cause string) {
	w.kill()
	output.GlobalLogger.Warning("PDF worker %s%s", cause, w.epitaph())
	p.spawn <- struct{}{}
}

// handBack decides a worker's fate from its answer: a broken engine or a lost
// stream is discarded, a document big enough to have grown the process retires
// it, and everything else goes back warm.
func (p *pdfWorkerPool) handBack(w *pdfWorker, ex pdfExchange, dataLen int) {
	switch {
	case errors.Is(ex.err, ErrPDFWorkerCrashed):
		p.discard(w, "died on the job")
	case ex.err != nil:
		p.discard(w, "failed the exchange")
	case ex.res.ErrCode == pdfErrRuntime:
		p.discard(w, "reported an engine failure")
	case int64(dataLen) > pdfRecycleInputBytes || totalPageBytes(ex.res.Pages) > pdfRecycleTextBytes:
		p.retire(w)
	default:
		p.release(w)
	}
}

// stale reports a worker that must not be handed a job: one that died while
// idle, or one idle long enough that its memory is worth more than its warmth.
func (p *pdfWorkerPool) stale(w *pdfWorker, now time.Time) bool {
	select {
	case <-w.exited:
		return true
	default:
	}
	return now.Sub(w.idleSince) >= p.timings.idle
}

// janitor retires idle workers on a timer. Without it a pool that went quiet
// keeps every process it ever started, and only the next PDF - which may never
// come - would notice.
func (p *pdfWorkerPool) janitor() {
	ticker := time.NewTicker(p.timings.sweep)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case now := <-ticker.C:
			p.sweepIdle(now)
		}
	}
}

// sweepIdle retires every idle worker that is stale and puts the rest back.
// Workers out on a job are not here to be touched. Sorting them is all the lock
// covers: a SIGKILL and the wait for the corpse must not stand between the jobs
// contending for this pool, and the permits the retirements hand back fit
// whatever the pool does meanwhile - live workers and outstanding permits
// together never exceed its size.
func (p *pdfWorkerPool) sweepIdle(now time.Time) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return // Close already killed them all
	}
	keep := make([]*pdfWorker, 0, p.size)
	var stale []*pdfWorker
drain:
	for {
		select {
		case w := <-p.idle:
			if p.stale(w, now) {
				stale = append(stale, w)
			} else {
				keep = append(keep, w)
			}
		default:
			break drain
		}
	}
	for _, w := range keep {
		p.idle <- w
	}
	p.mu.Unlock()

	for _, w := range stale {
		p.retire(w)
	}
}

// Close stops the janitor and kills every idle worker. It is the graceful
// shutdown path, to be called once the last analysis has drained: a worker
// still on a job is left alone and dies with the parent. The pool is never
// reopened - later jobs are refused with an engine error rather than quietly
// starting processes the shutdown just ended.
func (p *pdfWorkerPool) Close() {
	p.closeOnce.Do(func() { close(p.done) })
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for {
		select {
		case w := <-p.idle:
			w.kill()
		default:
			return
		}
	}
}

// ClosePDFWorkers shuts the PDF worker pool down if one was ever started, for a
// server draining before exit. It is one-way: the pool is not rebuilt, so any
// PDF reaching the checks afterwards is acknowledged as an unavailable engine.
// A one-shot CLI needs none of this - its workers die with it.
func ClosePDFWorkers() {
	pdfRuntime.mu.Lock()
	pool := pdfRuntime.pool
	pdfRuntime.mu.Unlock()
	if pool != nil {
		pool.Close()
	}
}

// pdfWorker is one extraction subprocess: this binary re-executed with the
// worker sentinel, answering one job at a time over a gob stream.
type pdfWorker struct {
	cmd  *exec.Cmd
	jobs *os.File // parent -> worker
	res  *os.File // worker -> parent
	dec  *gob.Decoder
	log  pdfWorkerLog
	// exited closes when the reaper goroutine has waited on the process, so a
	// worker that died on its own is reaped at once rather than at its next
	// job, and waitErr is safe to read from there on.
	exited    chan struct{}
	waitErr   error
	idleSince time.Time
}

func spawnPDFWorker(ctx context.Context, exe string, helloTimeout time.Duration) (*pdfWorker, error) {
	// A binary that never installed the sentinel would re-run its own program
	// instead of extracting, and one already running as a worker would fork a
	// tree of them. Both are the same mistake seen from two sides, and both are
	// read from what the handover recorded rather than re-derived from argv.
	if !pdfWorkerSentinelInstalled.Load() {
		return nil, errors.New("this binary does not dispatch the pdf worker sentinel")
	}
	if pdfWorkerSelf.Load() {
		return nil, errors.New("refusing to start a pdf worker from a pdf worker")
	}
	jobR, jobW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	resR, resW, err := os.Pipe()
	if err != nil {
		_ = jobR.Close()
		_ = jobW.Close()
		return nil, err
	}

	w := &pdfWorker{
		cmd:  exec.Command(exe, pdfWorkerArg),
		jobs: jobW,
		res:  resR,
		// Buffered: gob reads a message header a byte at a time, which is a
		// syscall each on a bare pipe.
		dec:    gob.NewDecoder(bufio.NewReader(resR)),
		exited: make(chan struct{}),
	}
	w.cmd.Stdin = jobR
	w.cmd.Stdout = resW
	// Stderr is kept, bounded, rather than discarded: it is the only channel a
	// worker has for the cause of its own death.
	w.cmd.Stderr = &w.log
	// One job at a time, so a worker's runtime needs no more than a core.
	w.cmd.Env = append(os.Environ(), "GOMAXPROCS=1")
	// A parent killed outright (SIGKILL, OOM killer) closes no pipes, so a
	// worker wedged inside wasm would never see EOF and would outlive it.
	setPDFWorkerDeathSignal(w.cmd)
	if err := w.cmd.Start(); err != nil {
		_ = jobR.Close()
		_ = jobW.Close()
		_ = resR.Close()
		_ = resW.Close()
		return nil, err
	}
	// The child owns its ends now. The parent must drop them, or the worker
	// never sees EOF and its death never closes the result pipe.
	_ = jobR.Close()
	_ = resW.Close()
	go func() {
		w.waitErr = w.cmd.Wait()
		close(w.exited)
	}()

	// The handshake waits on its own goroutine so the caller's context bounds it
	// too: a worker still compiling must not hold a scan that has ended for the
	// rest of the hello timeout. Killing the worker is what ends the decode.
	hello := make(chan error, 1)
	go func() { hello <- w.waitHello(helloTimeout) }()
	select {
	case err := <-hello:
		if err != nil {
			w.kill()
			return nil, fmt.Errorf("%v%s", err, w.epitaph())
		}
	case <-ctx.Done():
		w.kill()
		return nil, ctxCause(ctx)
	}
	return w, nil
}

// pdfCacheNoteOnce keeps a worker's non-fatal startup note to one line per
// process: every worker of a run reports the same one, and one per PDF would
// bury the scan's own output.
var pdfCacheNoteOnce sync.Once

// waitHello blocks until the worker reports a runtime it can extract with. A
// worker is not usable before it: dispatching to a process still compiling
// would spend the job's own timeout on the compile.
func (w *pdfWorker) waitHello(timeout time.Duration) error {
	if err := w.res.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("pdf worker handshake: %v", err)
	}
	var hello pdfHello
	if err := w.dec.Decode(&hello); err != nil {
		return fmt.Errorf("pdf worker did not start: %v", err)
	}
	if err := w.res.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("pdf worker handshake: %v", err)
	}
	if hello.Err != "" {
		return errors.New(hello.Err)
	}
	if hello.ProtocolVersion != pdfProtocolVersion {
		return fmt.Errorf("pdf worker speaks protocol %d, this binary speaks %d", hello.ProtocolVersion, pdfProtocolVersion)
	}
	// A worker that started, but not on the terms it wanted: usable, and this
	// is the only place an operator hears what it settled for.
	if hello.Note != "" {
		pdfCacheNoteOnce.Do(func() { output.GlobalLogger.Warning("PDF worker: %s", hello.Note) })
	}
	return nil
}

// writeJob frames one job onto the pipe: the fixed header, then the document
// bytes as they are. Nothing copies the document into an encoder buffer.
func (w *pdfWorker) writeJob(job pdfJob) error {
	var hdr [pdfJobHeaderBytes]byte
	binary.LittleEndian.PutUint64(hdr[0:], uint64(len(job.Data)))
	binary.LittleEndian.PutUint64(hdr[8:], uint64(job.MaxPages))
	binary.LittleEndian.PutUint64(hdr[16:], uint64(job.MaxTextBytes))
	binary.LittleEndian.PutUint64(hdr[24:], uint64(job.TimeoutNanos))
	if _, err := w.jobs.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.jobs.Write(job.Data)
	return err
}

// kill ends a worker and waits for it to be reaped. Closing both pipes unblocks
// the exchange goroutine that may still be writing a job or waiting for its
// answer.
func (w *pdfWorker) kill() {
	_ = w.cmd.Process.Kill()
	_ = w.jobs.Close()
	_ = w.res.Close()
	<-w.exited
}

// epitaph is the worker's exit status and the tail of its stderr, empty when it
// left neither. Only meaningful once the process has been reaped.
func (w *pdfWorker) epitaph() string {
	var b strings.Builder
	var exitErr *exec.ExitError
	if errors.As(w.waitErr, &exitErr) {
		fmt.Fprintf(&b, " (%v)", exitErr)
	}
	if tail := w.log.tail(); tail != "" {
		fmt.Fprintf(&b, ": %s", tail)
	}
	return b.String()
}

// pdfWorkerLog keeps the last pdfWorkerLogBytes a worker wrote to stderr: a
// wasm compile failure or a runtime's dying words are worth reporting, an
// unbounded child's output is not worth holding.
type pdfWorkerLog struct {
	mu  sync.Mutex
	buf []byte
}

func (l *pdfWorkerLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if len(l.buf) > pdfWorkerLogBytes {
		l.buf = append(l.buf[:0], l.buf[len(l.buf)-pdfWorkerLogBytes:]...)
	}
	return len(p), nil
}

func (l *pdfWorkerLog) tail() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.TrimSpace(string(l.buf))
}

// pdfRuntimeInitialized reports whether the lazy runtime was ever started
// (test hook for the zero-PDF-run guarantee). Atomic so the probe is safe
// from any goroutine, without taking the init mutex.
func pdfRuntimeInitialized() bool {
	return pdfRuntime.initialized.Load()
}

// ctxCause is ctx.Err() with a guaranteed non-nil result: a Done channel that
// closed without an error must not surface as success with no content.
func ctxCause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errPDFCancelled
}

// totalPageBytes sums the extracted text, which is what decides whether a
// worker kept enough memory to be worth replacing.
func totalPageBytes(pages [][]byte) int64 {
	var n int64
	for _, p := range pages {
		n += int64(len(p))
	}
	return n
}

// ReadPDF extracts per-page text (findings can cite "page N"; archive members
// flatten the blocks). Pages beyond limits.MaxPages and text beyond
// limits.MaxTextBytes truncate (partial content is still returned for
// scanning); the timeout aborts with ErrPDFTimeout and no content. Unreadable
// single pages are skipped, the rest of the document still scans.
//
// ctx is the second bound: a caller that cancelled or ran out of deadline
// aborts the scan at the next checkpoint, with no content and ctx.Err(). With
// both bounds tripped the cancellation is what is reported - a scan nobody
// waits for must not be filed as a document that timed out. ctx must be
// non-nil.
func ReadPDF(ctx context.Context, data []byte, limits PDFLimits) (pages [][]byte, truncated bool, err error) {
	pages, truncated, _, err = readPDF(ctx, data, limits)
	return pages, truncated, err
}

// pdfExchange is one job's trip over the wire: the worker's answer, or the
// stream error that ended it.
type pdfExchange struct {
	res pdfResult
	err error
}

// readPDF additionally reports how long extraction itself took, EXCLUDING
// the pool wait. Callers that meter cumulative PDF time (the archive
// iterator) must charge only this: queue time belongs to whichever archive
// held the worker, and charging it would let one package's PDFs consume
// another's budget - on a shared server, another tenant's.
//
// It is two measurements, deliberately: the worker's own extraction time when
// the worker answered (whatever it answered), and the parent's wall-clock since
// the worker was acquired when the parent gave up on it - a killed or abandoned
// job has no self-reported time, and reporting zero would make a wedged
// document look free to the budget that exists to bound it.
func readPDF(ctx context.Context, data []byte, limits PDFLimits) (pages [][]byte, truncated bool, extractTime time.Duration, err error) {
	if limits.MaxPages <= 0 || limits.MaxTextBytes <= 0 || limits.MaxFileBytes <= 0 {
		return nil, false, 0, fmt.Errorf("pdf limits must be positive")
	}
	// Before the pool: an oversized document must not be what first starts a
	// worker, nor cross a pipe to be rejected on the far side. MaxPDFInputBytes
	// is the sandbox backstop; the configured gate is normally far stricter.
	if int64(len(data)) > min(limits.MaxFileBytes, MaxPDFInputBytes) {
		return nil, false, 0, ErrPDFTooLarge
	}
	// Checked before the pool: a caller that has already given up must not pay
	// for runtime init or occupy a worker.
	if cerr := ctx.Err(); cerr != nil {
		return nil, false, 0, cerr
	}
	pool, err := pdfPool()
	if err != nil {
		return nil, false, 0, fmt.Errorf("%w: %v", ErrPDFRuntime, err)
	}

	timeout := limits.Timeout
	if timeout <= 0 {
		timeout = DefaultPDFTimeout
	}

	w, err := pool.acquire(ctx)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			if errors.Is(err, cerr) {
				return nil, false, 0, cerr
			}
			// A pool that failed for its own reasons around a cancellation
			// would otherwise be invisible.
			return nil, false, 0, fmt.Errorf("%w (worker: %v)", cerr, err)
		}
		return nil, false, 0, fmt.Errorf("%w: %v", ErrPDFRuntime, err)
	}
	// The clock starts only once a worker is held, for the same reason
	// acquisition waits on the caller's context alone.
	start := time.Now()

	// The pre-flight gate is stale by the time a worker is held: a context that
	// ended while we queued must not pay for a document parse.
	if cerr := ctx.Err(); cerr != nil {
		pool.release(w) // untouched, so still fit for the next job
		return nil, false, time.Since(start), cerr
	}

	job := pdfJob{
		Data:         data,
		MaxPages:     limits.MaxPages,
		MaxTextBytes: limits.MaxTextBytes,
		TimeoutNanos: int64(timeout),
	}
	// The exchange runs on its own goroutine so a wedged worker cannot pin the
	// caller: a job too big for the pipe buffer blocks until the worker reads
	// it, and the answer blocks until it replies. Killing the worker ends both.
	// A worker that could not take the whole job was broken before the document
	// reached it; one that took it and hung up most likely died of it. Any
	// other unreadable answer came from a live worker, so the engine is broken.
	done := make(chan pdfExchange, 1)
	go func() {
		if werr := w.writeJob(job); werr != nil {
			done <- pdfExchange{err: fmt.Errorf("%w: %v", ErrPDFRuntime, werr)}
			return
		}
		var res pdfResult
		if rerr := w.dec.Decode(&res); rerr != nil {
			cause := ErrPDFRuntime
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				cause = ErrPDFWorkerCrashed
			}
			done <- pdfExchange{err: fmt.Errorf("%w: %v", cause, rerr)}
			return
		}
		done <- pdfExchange{res: res}
	}()
	// From the job write, not from acquisition: the worker greeted the parent
	// before it was handed out, so nothing but the extraction and the document's
	// own trip through the pipe is inside this. The grace covers that trip - a
	// second per 64 MiB, which no configured gate comes near - so a large
	// document cannot be read as a worker that stopped answering.
	timer := time.NewTimer(timeout + pool.timings.grace + time.Duration(len(data))*time.Second/(64<<20))
	defer timer.Stop()

	select {
	case ex := <-done:
		pool.handBack(w, ex, len(data))
		// The answer and the cancellation can both be ready here, and the select
		// above would report either: the cancellation outranks the extraction's
		// own outcome, so it is decided here rather than by scheduling.
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, time.Since(start), cerr
		}
		if ex.err != nil {
			return nil, false, time.Since(start), ex.err
		}
		return ex.res.Pages, ex.res.Truncated, time.Duration(ex.res.ExtractNanos), pdfErrFromWire(ex.res.ErrCode, ex.res.Detail)
	case <-ctx.Done():
		cerr := ctxCause(ctx)
		// The content is discarded either way, but an answer already on its way
		// keeps the worker: a cancelled request is routine, and must not cost a
		// process. Only a worker still silent after the grace is killed.
		select {
		case ex := <-done:
			pool.handBack(w, ex, len(data))
		case <-time.After(pool.timings.grace):
			pool.retire(w)
			<-done
		}
		return nil, false, time.Since(start), cerr
	case <-timer.C:
		// Past its own deadline plus the grace, the worker is not extracting
		// any more, it is stuck: nothing but the signal ends it.
		pool.discard(w, "killed: no answer within the job timeout")
		<-done
		return nil, false, time.Since(start), ErrPDFTimeout
	}
}
