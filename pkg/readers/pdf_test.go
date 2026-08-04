package readers

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// NOTE: this test must stay FIRST in this file, and no other test file in the
// package may touch the PDF runtime: it asserts the lazy-init guarantee (a
// run that never sees a PDF must never pay for the wasm runtime).
func TestPDFRuntimeLazyBeforeFirstUse(t *testing.T) {
	assert.False(t, pdfRuntimeInitialized(), "PDF runtime must not initialize before the first PDF")
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

var testPDFLimits = PDFLimits{MaxPages: 500, MaxTextBytes: 10 * 1024 * 1024, Timeout: 30 * time.Second}

func TestReadPDFExtractsPerPageText(t *testing.T) {
	data := writeMinimalPDF("alpha secret on page one", "beta token on page two")
	pages, truncated, err := ReadPDF(data, testPDFLimits)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, pages, 2)
	assert.Contains(t, string(pages[0]), "alpha secret")
	assert.Contains(t, string(pages[1]), "beta token")
}

func TestReadPDFPageCap(t *testing.T) {
	data := writeMinimalPDF("page one text", "page two text", "page three hidden", "page four hidden", "page five hidden")
	limits := testPDFLimits
	limits.MaxPages = 2
	pages, truncated, err := ReadPDF(data, limits)
	assert.NoError(t, err)
	assert.True(t, truncated, "pages beyond the cap must truncate")
	assert.Len(t, pages, 2)
	joined := string(bytes.Join(pages, []byte(" ")))
	assert.Contains(t, joined, "page one")
	assert.NotContains(t, joined, "hidden", "content past the page cap must not be extracted")
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
	_, _, err := ReadPDF(data, PDFLimits{MaxPages: 0, MaxTextBytes: 1024})
	assert.Error(t, err)
	_, _, err = ReadPDF(data, PDFLimits{MaxPages: 10, MaxTextBytes: 0})
	assert.Error(t, err)
}

func TestReadPDFConcurrentBatch(t *testing.T) {
	// Pool + Once under concurrency: more goroutines than pool instances.
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
