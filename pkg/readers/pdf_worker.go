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
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	pdfium "github.com/klippa-app/go-pdfium"
	pdfium_errors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// pdfWorkerArg turns a pc binary into an extraction worker. An argv sentinel
// rather than a flag: it is how the pool starts its own processes, not CLI
// surface, and the underscores keep it clear of any real argument.
const pdfWorkerArg = "__pc-pdf-worker"

// pdfProtocolVersion is the wire contract between parent and worker. A worker
// that answers with another version is refused, which is what keeps a binary
// replaced under a running process from being spoken to in a dialect it does
// not have. Bump it for any change to the job header layout, the pdfResult
// fields, or what an error code means.
const pdfProtocolVersion uint8 = 2

// pdfJobHeaderBytes is the fixed little-endian job header: data length, page
// ceiling, text budget, timeout - four int64s, then the document itself.
const pdfJobHeaderBytes = 32

// pdfWorkerSentinelInstalled records that this binary hands over to the worker
// loop before doing anything else, and pdfWorkerSelf that this process IS one.
// The pool re-executes the running binary: one that never installed the
// sentinel would re-run its own program (a test suite, a scan) instead of
// extracting, and a worker starting workers would fork a tree of them. Both are
// recorded at the handover rather than re-read from argv, which is not the
// process's own answer to either question.
var (
	pdfWorkerSentinelInstalled atomic.Bool
	pdfWorkerSelf              atomic.Bool
)

// pdfHello is the worker's first message: it has a runtime and can take jobs,
// or it has none and says why, and dies. Nothing is dispatched before it. Note
// carries what a worker settled for without failing - a runtime it can extract
// with, built on worse terms than it asked for.
type pdfHello struct {
	ProtocolVersion uint8
	Err             string
	Note            string
}

// pdfJob is one extraction request. It is framed by hand rather than gob-
// encoded: the document is the bulk of every job, and gob would copy all of it
// into an encoder buffer on the way out and another on the way in. The answer
// IS gob and does pay that buffer for the extracted text: the decision here is
// about the input document only.
type pdfJob struct {
	Data         []byte
	MaxPages     int
	MaxTextBytes int64
	TimeoutNanos int64
}

// pdfResult is one answer. ExtractNanos is the extraction alone - the parent
// charges an archive's PDF budget with it, and neither the queue wait nor the
// trip over the pipe belongs there. Detail carries what the code cannot: the
// page count that was too high, the engine failure's own words.
type pdfResult struct {
	Pages        [][]byte
	Truncated    bool
	ExtractNanos int64
	ErrCode      uint8
	Detail       string
}

// Wire codes for the outcome: an error value does not survive a gob round trip
// as itself, so every sentinel the worker can report gets a code and keeps its
// identity on the far side. Everything else travels as text under pdfErrOther.
// ErrPDFTooLarge has no code: the parent gates on size before shipping bytes,
// so a worker never sees an oversized document.
const (
	pdfErrNone uint8 = iota
	pdfErrPassword
	pdfErrTimeout
	pdfErrTooManyPages
	pdfErrRuntime
	pdfErrOther
)

// pdfErrFromWire rebuilds the error the caller sees: callers match sentinels
// with errors.Is and print the text, so both have to come back.
func pdfErrFromWire(code uint8, detail string) error {
	var sentinel error
	switch code {
	case pdfErrNone:
		return nil
	case pdfErrPassword:
		sentinel = ErrPDFPassword
	case pdfErrTimeout:
		sentinel = ErrPDFTimeout
	case pdfErrTooManyPages:
		sentinel = ErrPDFTooManyPages
	case pdfErrRuntime:
		sentinel = ErrPDFRuntime
	default:
		if detail == "" {
			return errors.New("pdf worker reported an unspecified error")
		}
		return errors.New(detail)
	}
	if detail == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, detail)
}

// HandlePDFWorkerSentinel hands this process over to the worker loop when it
// was started as one, and exits when the loop ends - a caller cannot forget to.
// Every pc entry point calls it before anything else, including the TestMain of
// every package whose tests drive the check pipeline: the pool re-executes the
// running binary, so whichever main the child lands in has to hand over.
func HandlePDFWorkerSentinel() {
	pdfWorkerSentinelInstalled.Store(true)
	if len(os.Args) > 1 && os.Args[1] == pdfWorkerArg {
		pdfWorkerSelf.Store(true)
		runPDFWorker()
		os.Exit(0)
	}
}

// runPDFWorker greets the parent, then answers framed extraction jobs on stdin
// with gob-encoded results on stdout until the parent closes the pipe. A
// runtime that failed to build is reported in the greeting and ends the
// process: a worker that cannot extract must die rather than answer every job
// of a scan with the same failure.
func runPDFWorker() {
	// Results own fd 1 from here on: any stray write to os.Stdout - a logger, a
	// library banner - would corrupt the stream mid-message. Stderr is where
	// the parent collects worker chatter.
	results := os.Stdout
	os.Stdout = os.Stderr

	enc := gob.NewEncoder(results)
	pool, note, err := initPDFWorkerRuntime()
	if err != nil {
		_ = enc.Encode(&pdfHello{ProtocolVersion: pdfProtocolVersion, Err: err.Error()})
		return
	}
	if err := enc.Encode(&pdfHello{ProtocolVersion: pdfProtocolVersion, Note: note}); err != nil {
		return
	}

	in := bufio.NewReader(os.Stdin)
	var buf []byte
	for {
		job, err := readPDFJob(in, &buf)
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return // the parent closed the pipe or died: nothing left to answer
		case err != nil:
			// A stream this worker cannot make sense of - only a protocol fault
			// gets here. With the job already in the pipe the parent reads the
			// exit as a crash on the document, so stderr is the one place that
			// says what was actually refused.
			fmt.Fprintf(os.Stderr, "pdf worker: %v\n", err)
			os.Exit(2)
		}
		res := runPDFJob(pool, job)
		if err := enc.Encode(&res); err != nil {
			return
		}
	}
}

// readPDFJob reads one framed job into a buffer that outlives it: documents are
// read back to back, and the parent retires a worker whose document was big
// enough for the buffer to be worth freeing.
func readPDFJob(r io.Reader, buf *[]byte) (pdfJob, error) {
	var hdr [pdfJobHeaderBytes]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return pdfJob{}, err
	}
	dataLen := int64(binary.LittleEndian.Uint64(hdr[0:]))
	// A length outside the parent's own admission gate means the stream is not
	// what this worker thinks it is; answering would be worse than dying.
	if dataLen < 0 || dataLen > MaxPDFInputBytes {
		return pdfJob{}, fmt.Errorf("job declares %d bytes, past the %d-byte admission gate", dataLen, int64(MaxPDFInputBytes))
	}
	if int64(cap(*buf)) < dataLen {
		*buf = make([]byte, dataLen)
	}
	job := pdfJob{
		Data:         (*buf)[:dataLen],
		MaxPages:     int(binary.LittleEndian.Uint64(hdr[8:])),
		MaxTextBytes: int64(binary.LittleEndian.Uint64(hdr[16:])),
		TimeoutNanos: int64(binary.LittleEndian.Uint64(hdr[24:])),
	}
	if _, err := io.ReadFull(r, job.Data); err != nil {
		return pdfJob{}, err
	}
	return job, nil
}

// initPDFWorkerRuntime builds the worker's wasm pool: one instance, reused for
// the life of the process. Both settings are what the subprocess buys.
// CloseOnContextDone is OFF - its termination checkpoints cost ~2.3x measured
// on pdfium's hot loops (92 ms vs 39 ms for a 50-page document), and the
// interruption they enable is the parent's SIGKILL now. ReuseWorkers is ON, so
// pdfium's ~18 MB of linear memory is zeroed once per process instead of once
// per document.
func initPDFWorkerRuntime() (pdfium.Pool, string, error) {
	runtimeConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pdfWasmMemoryLimitPages)
	var note string
	cache, err := pdfCompilationCache()
	if err != nil {
		// Not fatal, but not free either: every worker then recompiles the
		// module, and the parent is the only place that can say so.
		note = fmt.Sprintf("compilation cache unavailable: %v; compiling in memory", err)
	} else {
		runtimeConfig = runtimeConfig.WithCompilationCache(cache)
	}
	pool, err := webassembly.Init(webassembly.Config{
		RuntimeConfig: runtimeConfig,
		// One instance, pre-created at init so the first job does not pay for
		// it, and never destroyed: this process handles one job at a time.
		MinIdle:      1,
		MaxIdle:      1,
		MaxTotal:     1,
		ReuseWorkers: true,
		// Empty FSConfig: the library default would mount the HOST ROOT into
		// the sandbox. Input is bytes-only; no filesystem.
		FSConfig: wazero.NewFSConfig(),
		// pdfium chatter must never reach the result pipe.
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	return pool, note, err
}

// pdfCompilationCache is the disk-backed wazero cache: without it every worker
// process recompiles the module (~2.4 s); with it, startup is <100 ms. wazero
// namespaces the directory by its own version and CPU features. A directory
// that cannot be used is not fatal - compiling in memory is slow, but correct -
// so the cause travels back with the nil cache instead of being swallowed.
func pdfCompilationCache() (wazero.CompilationCache, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, "pc", "wazero")
	sweepStaleCacheTemps(root)
	cache, err := wazero.NewCompilationCacheWithDir(root)
	if err != nil {
		return nil, err
	}
	return cache, nil
}

// sweepStaleCacheTemps removes cache entries left half-written. wazero writes
// each compiled module to a temp file and renames it into place, so a worker
// SIGKILLed mid-compile strands one, and nothing else ever cleans them up. The
// hour of grace keeps a live compile in another process safe.
func sweepStaleCacheTemps(root string) {
	versions, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, version := range versions {
		if !version.IsDir() {
			continue
		}
		dir := filepath.Join(root, version.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// runPDFJob answers one job. The instance is borrowed per job and handed back
// (ReuseWorkers keeps it alive), which is also what closes whatever pdfium
// still holds from the document just read.
func runPDFJob(pool pdfium.Pool, job pdfJob) pdfResult {
	start := time.Now()
	// The pool holds exactly one instance for this process alone, so there is
	// nothing to queue behind and no wait to bound.
	instance, err := pool.GetInstanceWithContext(context.Background())
	if err != nil {
		return pdfResult{ExtractNanos: int64(time.Since(start)), ErrCode: pdfErrRuntime, Detail: err.Error()}
	}
	defer instance.Close()

	pages, truncated, code, detail := extractPDF(instance, job, start.Add(time.Duration(job.TimeoutNanos)))
	return pdfResult{
		Pages:        pages,
		Truncated:    truncated,
		ExtractNanos: int64(time.Since(start)),
		ErrCode:      code,
		Detail:       detail,
	}
}

// extractPDF is the extraction itself: the page-count gate, the page loop and
// the text cap, on one pdfium instance. Pages beyond the text budget truncate
// (partial content is still returned for scanning); the deadline aborts with
// pdfErrTimeout and no content, because partial-on-timeout would make findings
// depend on machine speed. Unreadable single pages are skipped, the rest of the
// document still scans. The outcome is reported as a wire code and its detail
// rather than an error: those are what cross the pipe, and rebuilding one from
// the other's text is how detail gets lost.
//
// The deadline is polled where the in-process engine polled it. A document that
// behaves ends through these points and leaves the process fit for the next
// job; only one wedged inside wasm reaches the parent's SIGKILL.
func extractPDF(instance pdfium.Pdfium, job pdfJob, deadline time.Time) (pages [][]byte, truncated bool, code uint8, detail string) {
	expired := func() bool { return !time.Now().Before(deadline) }
	if expired() {
		return nil, false, pdfErrTimeout, ""
	}

	docRes, err := instance.OpenDocument(&requests.OpenDocument{File: &job.Data})
	if err != nil {
		if errors.Is(err, pdfium_errors.ErrPassword) {
			return nil, false, pdfErrPassword, ""
		}
		if expired() {
			return nil, false, pdfErrTimeout, ""
		}
		return nil, false, pdfErrOther, err.Error()
	}
	defer func() {
		_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: docRes.Document})
	}()

	countRes, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: docRes.Document})
	if err != nil {
		if expired() {
			return nil, false, pdfErrTimeout, ""
		}
		return nil, false, pdfErrOther, err.Error()
	}
	// All-or-nothing: reject before loading a single page. The count is ~1% of
	// an extraction, so a long document is cheap to turn away, and the caller
	// never has to reason about a partially-scanned report.
	if countRes.PageCount > job.MaxPages {
		return nil, false, pdfErrTooManyPages, fmt.Sprintf("%d pages", countRes.PageCount)
	}

	pages = make([][]byte, 0, countRes.PageCount)
	var total int64
	for i := 0; i < countRes.PageCount; i++ {
		if expired() {
			return nil, false, pdfErrTimeout, ""
		}
		tp, err := instance.FPDFText_LoadPage(&requests.FPDFText_LoadPage{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: docRes.Document, Index: i}},
		})
		if err != nil {
			if expired() {
				return nil, false, pdfErrTimeout, ""
			}
			// Unreadable page: scan the rest, but keep its placeholder so
			// later findings still cite the right "page N".
			pages = append(pages, nil)
			continue
		}

		// Raw single-call extraction: the convenience GetPageText crosses the
		// wasm boundary once PER CHARACTER. Chars are clamped to the remaining
		// byte budget (>= 1 byte per char), so a dense page cannot overshoot
		// the cap by more than its UTF-8 expansion.
		remaining := job.MaxTextBytes - total
		var pageText string
		if cc, err := instance.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: tp.TextPage}); err == nil && cc.Count > 0 {
			n := cc.Count
			if int64(n) > remaining {
				n = int(remaining)
				truncated = true
			}
			if res, err := instance.FPDFText_GetText(&requests.FPDFText_GetText{TextPage: tp.TextPage, StartIndex: 0, Count: n}); err == nil {
				pageText = res.Text
			}
		}
		_, _ = instance.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})

		if pageText != "" {
			b := []byte(pageText)
			if int64(len(b)) > remaining {
				b = b[:remaining]
				truncated = true
			}
			pages = append(pages, b)
			total += int64(len(b))
			if total >= job.MaxTextBytes {
				truncated = true
				break
			}
		} else {
			// Keep page indexes aligned with the document for "page N"
			// reporting even when a page carries no text.
			pages = append(pages, nil)
		}
	}

	return pages, truncated, pdfErrNone, ""
}
