// Excluded under -race: the watchdog's instance.Kill is deliberately
// lock-free in go-pdfium ("Kill is a last-effort to recover a broken
// process") and by design races the entry check of whatever wasm call it
// interrupts - that pair cannot be synchronized without blocking Kill for
// the duration of the call, which would defeat the interruption. ReadPDF's
// own cleanup (document + instance Close) IS serialized against Kill; only
// the library-internal, recover-contained pair remains.
//
//go:build !race

package readers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReadPDFTimeoutDiscardsContent(t *testing.T) {
	// A 1 ns budget expires before the first between-page deadline check no
	// matter how the watchdog goroutine is scheduled, so the outcome is
	// deterministic; the watchdog Kill racing document open exercises the
	// Kill/Close serialization.
	data := writeMinimalPDF("timeout page one", "timeout page two")
	limits := testPDFLimits
	limits.Timeout = time.Nanosecond
	pages, truncated, err := ReadPDF(context.Background(), data, limits)
	assert.ErrorIs(t, err, ErrPDFTimeout)
	assert.Nil(t, pages, "timeout must discard partial content (determinism)")
	assert.False(t, truncated)
}
