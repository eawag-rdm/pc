package readers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	pdfium "github.com/klippa-app/go-pdfium"
	pdfium_errors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/eawag-rdm/pc/pkg/output"
)

// PDFLimits bounds PDF text extraction. Like ArchiveLimits it fails closed:
// non-positive page or byte limits mean nothing is extracted.
//
// The three layers each bound what only they can: MaxPages is the sole
// deterministic CPU bound (scanned PDFs extract ~0 bytes and never trip byte
// caps), MaxTextBytes kills dense-text amplification, and Timeout is the
// backstop against pathological files (kept internal so results stay
// deterministic for everything else).
type PDFLimits struct {
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
	// ErrPDFTooLarge marks documents rejected by the MaxPDFInputBytes gate
	// before any extraction work (distinct ack: "parse error" would mislead).
	ErrPDFTooLarge = errors.New("pdf too large for sandbox")
	// ErrPDFRuntime marks a wasm runtime that failed to initialize (compile
	// failure, unwritable cache, OOM). The failure is memoized, so without a
	// distinct sentinel every PDF in the run would report a bogus parse
	// error instead of the one real cause.
	ErrPDFRuntime = errors.New("pdf engine unavailable")
)

// pdfRuntime is the lazily-initialized shared wasm runtime. A run without
// PDFs never pays for it. The Once memoizes failures too: every later PDF
// gets the same error (skip ack) instead of a per-file init retry storm.
var pdfRuntime struct {
	once        sync.Once
	pool        pdfium.Pool
	err         error
	initialized atomic.Bool
}

func pdfPool() (pdfium.Pool, error) {
	pdfRuntime.once.Do(func() {
		runtimeConfig := wazero.NewRuntimeConfig().
			// Kill() can only interrupt in-flight wasm with this set. The
			// termination checkpoints wazero compiles in are NOT free:
			// measured ~2.3x on pdfium's hot loops (~92 ms vs ~39 ms for a
			// 50-page document). Deliberate trade - without it a
			// pathological page pins a pool slot and OS thread forever,
			// which the long-lived server cannot afford. If PDF-heavy CLI
			// wall-clock ever matters, this flag is where half the time
			// goes.
			WithCloseOnContextDone(true).
			WithMemoryLimitPages(pdfWasmMemoryLimitPages)
		// Disk-backed compilation cache: without it every CLI run recompiles
		// the module (~2.4 s); with it, warm init is <100 ms. wazero
		// namespaces the directory by its own version and CPU features.
		if dir, err := os.UserCacheDir(); err == nil {
			cache, cerr := wazero.NewCompilationCacheWithDir(filepath.Join(dir, "pc", "wazero"))
			if cerr == nil {
				runtimeConfig = runtimeConfig.WithCompilationCache(cache)
			} else {
				output.GlobalLogger.Warning("PDF: compilation cache unavailable (%v); compiling in memory", cerr)
			}
		}
		pdfRuntime.pool, pdfRuntime.err = webassembly.Init(webassembly.Config{
			RuntimeConfig: runtimeConfig,
			MinIdle:       0,
			MaxIdle:       1,
			// Small fixed pool, NOT NumCPU: check workers block on
			// acquisition as backpressure; instance memory scales with the
			// largest in-flight document.
			MaxTotal: min(4, runtime.NumCPU()),
			// Default (false) destroys the instance per document - frees
			// wasm memory every file; replacement costs single-digit ms.
			ReuseWorkers: false,
			// Empty FSConfig: the library default would mount the HOST ROOT
			// into the sandbox. Input is bytes-only; no filesystem.
			FSConfig: wazero.NewFSConfig(),
			// pdfium chatter must not corrupt -json/TUI output.
			Stdout: io.Discard,
			Stderr: io.Discard,
		})
		pdfRuntime.initialized.Store(true)
	})
	return pdfRuntime.pool, pdfRuntime.err
}

// pdfRuntimeInitialized reports whether the lazy runtime was ever started
// (test hook for the zero-PDF-run guarantee). Atomic so the probe is safe
// from any goroutine, without touching the Once.
func pdfRuntimeInitialized() bool {
	return pdfRuntime.initialized.Load()
}

// ReadPDF extracts per-page text (findings can cite "page N"; archive members
// flatten the blocks). Pages beyond limits.MaxPages and text beyond
// limits.MaxTextBytes truncate (partial content is still returned for
// scanning); the timeout aborts with ErrPDFTimeout and no content. Unreadable
// single pages are skipped, the rest of the document still scans.
func ReadPDF(data []byte, limits PDFLimits) (pages [][]byte, truncated bool, err error) {
	if limits.MaxPages <= 0 || limits.MaxTextBytes <= 0 {
		return nil, false, fmt.Errorf("pdf limits must be positive")
	}
	// Before pool init: an oversized document must not be what first pays
	// the runtime compile.
	if int64(len(data)) > MaxPDFInputBytes {
		return nil, false, ErrPDFTooLarge
	}
	pool, err := pdfPool()
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrPDFRuntime, err)
	}

	timeout := limits.Timeout
	if timeout <= 0 {
		timeout = DefaultPDFTimeout
	}

	// Acquisition blocks without a deadline of its own: pool wait is
	// backpressure, not extraction time - a queue must not surface as a
	// spurious "timed out" ack. The clock therefore starts only after an
	// instance is held.
	instance, err := pool.GetInstanceWithContext(context.Background())
	if err != nil {
		return nil, false, err
	}

	deadline := time.Now().Add(timeout)

	// Watchdog: Kill interrupts in-flight wasm (CloseOnContextDone above).
	// Kill is deliberately lock-free in go-pdfium and races the instance's
	// own cleanup calls, so the two sides are serialized here: whichever
	// takes the mutex first wins, and the document/instance Close pair is
	// skipped entirely once Kill ran (Kill already invalidates the worker,
	// which frees the pool slot).
	var doc *responses.OpenDocument
	var watchdogMu sync.Mutex
	var killed, finished bool
	watchdogDone := make(chan struct{})
	defer func() {
		watchdogMu.Lock()
		finished = true
		doClose := !killed
		watchdogMu.Unlock()
		close(watchdogDone)
		if doClose {
			if doc != nil {
				_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
			}
			instance.Close()
		}
	}()
	go func() {
		select {
		case <-watchdogDone:
		case <-time.After(timeout):
			watchdogMu.Lock()
			if !finished {
				killed = true
				_ = instance.Kill()
			}
			watchdogMu.Unlock()
		}
	}()

	expired := func() bool { return !time.Now().Before(deadline) }

	docRes, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		if errors.Is(err, pdfium_errors.ErrPassword) {
			return nil, false, ErrPDFPassword
		}
		if expired() {
			return nil, false, ErrPDFTimeout
		}
		return nil, false, err
	}
	doc = docRes

	countRes, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		if expired() {
			return nil, false, ErrPDFTimeout
		}
		return nil, false, err
	}
	scanPages := countRes.PageCount
	if scanPages > limits.MaxPages {
		scanPages = limits.MaxPages
		truncated = true
	}

	var total int64
	for i := 0; i < scanPages; i++ {
		if expired() {
			return nil, false, ErrPDFTimeout
		}
		tp, err := instance.FPDFText_LoadPage(&requests.FPDFText_LoadPage{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if err != nil {
			if expired() {
				return nil, false, ErrPDFTimeout
			}
			// Unreadable page: scan the rest, but keep its placeholder so
			// later findings still cite the right "page N".
			pages = append(pages, nil)
			continue
		}

		// Raw single-call extraction: the convenience GetPageText crosses
		// the wasm boundary once PER CHARACTER. Chars are clamped to the
		// remaining byte budget (>= 1 byte per char), so a dense page
		// cannot overshoot the cap by more than its UTF-8 expansion.
		remaining := limits.MaxTextBytes - total
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
			if total >= limits.MaxTextBytes {
				truncated = true
				break
			}
		} else {
			// Keep page indexes aligned with the document for "page N"
			// reporting even when a page carries no text.
			pages = append(pages, nil)
		}
	}

	return pages, truncated, nil
}
