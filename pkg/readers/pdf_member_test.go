package readers

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/eawag-rdm/pc/pkg/structs"
)

type zipMember struct {
	name string
	data []byte
}

func writeZipFixture(t *testing.T, members []zipMember) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		w, err := zw.Create(m.name)
		assert.NoError(t, err)
		_, err = w.Write(m.data)
		assert.NoError(t, err)
	}
	assert.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "fixture.zip")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return path
}

var testMemberLimits = ArchiveLimits{
	MaxMemberSize:  10 * 1024 * 1024,
	MaxTotalMemory: 100 * 1024 * 1024,
	MaxMemberCount: 1000,
	MaxPDFPages:    500,
	MaxPDFFileSize: 5 * 1024 * 1024,
}

// drainIterator returns yielded member names keyed to their content.
func drainMembers(u *UnpackedFileIterator) map[string][]byte {
	got := map[string][]byte{}
	if !u.HasFilesToUnpack() {
		return got
	}
	for u.HasNext() {
		if !u.Next() {
			break
		}
		name, content, _ := u.UnpackedFile()
		got[name] = content
	}
	return got
}

// writeFlatePDF builds a single-page PDF whose content stream is
// FlateDecode-compressed, so the extracted text can vastly exceed the file's
// declared size (the shape the member text cap exists for). Text must not
// contain (, ) or backslashes.
func writeFlatePDF(t *testing.T, text string) []byte {
	t.Helper()
	raw := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	_, err := zw.Write([]byte(raw))
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())

	var buf bytes.Buffer
	offsets := []int{0}
	writeObj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%s\n", body)
	}
	buf.WriteString("%PDF-1.4\n")
	writeObj("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj")
	writeObj("2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj")
	writeObj("3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj")
	offsets = append(offsets, buf.Len())
	fmt.Fprintf(&buf, "4 0 obj << /Length %d /Filter /FlateDecode >> stream\n", zbuf.Len())
	buf.Write(zbuf.Bytes())
	buf.WriteString("\nendstream endobj\n")
	writeObj("5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj")

	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xrefStart)
	return buf.Bytes()
}

func TestPDFMemberExtractedInZip(t *testing.T) {
	pdf := writeMinimalPDF("clean cover", "archived secret token here")
	path := writeZipFixture(t, []zipMember{
		{"docs/report.pdf", pdf},
		{"readme.txt", []byte("plain text\n")},
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	got := drainMembers(u)
	assert.Contains(t, got, "docs/report.pdf")
	assert.Contains(t, string(got["docs/report.pdf"]), "archived secret token")
	assert.Contains(t, got, "readme.txt")
	assert.Empty(t, u.SkipMessages())
	// Extracted text is what gets charged, alongside the text member.
	expected := int64(len(got["docs/report.pdf"]) + len(got["readme.txt"]))
	assert.Equal(t, expected, u.totalMemoryUsed)
}

func TestPDFMemberZeroPageLimitFailsClosed(t *testing.T) {
	limits := testMemberLimits
	limits.MaxPDFPages = 0
	path := writeZipFixture(t, []zipMember{
		{"doc.pdf", writeMinimalPDF("hidden text")},
		{"readme.txt", []byte("plain text\n")},
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", limits, nil)
	got := drainMembers(u)
	assert.NotContains(t, got, "doc.pdf", "PDF member must fail closed without a page limit")
	assert.Contains(t, got, "readme.txt")
	assert.Empty(t, u.SkipMessages(), "fail-closed skip is silent")
}

func TestPDFMemberCancelledScanStaysSilent(t *testing.T) {
	// The caller gave up before this archive was walked, so the PDF member is
	// neither decompressed nor extracted - and must not be blamed for it: "PDF
	// could not be parsed" would accuse a perfectly readable document of the
	// caller's own cancellation. The walk ends with it, so the text member
	// behind it (which TestPDFMemberExtractedInZip yields from the same shape)
	// is not read either.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := writeZipFixture(t, []zipMember{
		{"doc.pdf", writeMinimalPDF("archived secret token here")},
		{"readme.txt", []byte("plain text\n")},
	})
	u := InitArchiveIterator(ctx, path, "fixture.zip", testMemberLimits, nil)
	assert.Empty(t, drainMembers(u), "a cancelled scan must yield nothing from the PDF member on")
	assert.Empty(t, u.SkipMessages(), "an extractable PDF member skipped for cancellation adds no ack")
}

func TestPDFMemberWallClockBudgetBreaker(t *testing.T) {
	path := writeZipFixture(t, []zipMember{
		{"a.pdf", writeMinimalPDF("first pdf")},
		{"keep.txt", []byte("still scanned\n")},
		{"b.pdf", writeMinimalPDF("second pdf")},
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	u.pdfWallTime = maxArchivePDFTime // budget already exhausted
	got := drainMembers(u)
	assert.NotContains(t, got, "a.pdf")
	assert.NotContains(t, got, "b.pdf")
	assert.Contains(t, got, "keep.txt", "non-PDF members must keep scanning")

	msgs := u.SkipMessages()
	assert.Len(t, msgs, 1, "exactly one archive-level ack, then silence")
	assert.Contains(t, msgs[0].Content, "Stopped PDF extraction for archive")
	src, ok := msgs[0].Source.(structs.File)
	assert.True(t, ok)
	assert.Equal(t, "fixture.zip", src.Name, "budget ack is archive-level")
	assert.Empty(t, src.ArchiveName, "archive-level source carries no parent archive")
}

func TestPDFMemberImageOnlyNotYielded(t *testing.T) {
	// Three pages, no text: placeholder pages must not merge into a member
	// of bare newlines.
	path := writeZipFixture(t, []zipMember{
		{"scan.pdf", writeMinimalPDF("", "", "")},
		{"readme.txt", []byte("plain text\n")},
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	got := drainMembers(u)
	assert.NotContains(t, got, "scan.pdf")
	assert.Contains(t, got, "readme.txt")
	assert.Equal(t, int64(len(got["readme.txt"])), u.totalMemoryUsed, "empty PDF must charge nothing")

	// Must be acknowledged, not silently dropped: an unscanned scan is not
	// the same report as a scanned-and-clean document.
	msgs := u.SkipMessages()
	assert.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "no extractable text")
}

func TestContentRoutedPDFMemberScanned(t *testing.T) {
	// The extension is the attacker's to choose: a genuine PDF named
	// without .pdf must still be extracted, not dropped as binary.
	pdf := writeMinimalPDF("secret token in a renamed pdf")
	path := writeZipFixture(t, []zipMember{
		{"attachment", pdf},
		{"report.pdf ", pdf}, // trailing space: extractors normalize it away
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	got := drainMembers(u)
	assert.Contains(t, string(got["attachment"]), "secret token")
	assert.Contains(t, string(got["report.pdf "]), "secret token")
}

func TestContentRoutedNonPDFStaysSilent(t *testing.T) {
	// Binary member that merely contains the magic bytes: routed, fails to
	// parse, and must stay as silent as any other binary member.
	junk := append(bytes.Repeat([]byte{0x00, 0x13}, 64), []byte("%PDF-1.4 not really")...)
	junk = append(junk, bytes.Repeat([]byte{0x42, 0x00}, 512)...)
	path := writeZipFixture(t, []zipMember{{"blob.bin", junk}})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	got := drainMembers(u)
	assert.NotContains(t, got, "blob.bin")
	assert.Empty(t, u.SkipMessages(), "content-routed non-PDF must not add ack noise")
}

func TestPDFMemberMisnamedBinarySilent(t *testing.T) {
	junk := bytes.Repeat([]byte{0x00, 0x13, 0x42, 0x99}, 2048) // no magic, not text
	path := writeZipFixture(t, []zipMember{
		{"fake.pdf", junk},
		{"readme.txt", []byte("plain text\n")},
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	got := drainMembers(u)
	assert.NotContains(t, got, "fake.pdf")
	assert.Contains(t, got, "readme.txt")
	assert.Empty(t, u.SkipMessages())
}

func TestPDFMemberMisnamedTextBuffered(t *testing.T) {
	text := []byte("plain text with a secret keyword inside\n" + strings.Repeat("filler line\n", 200))
	path := writeZipFixture(t, []zipMember{{"notes.pdf", text}})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
	got := drainMembers(u)
	assert.Equal(t, text, got["notes.pdf"], "text member misnamed .pdf must be raw-buffered in full")
}

func TestPDFMemberTextCapTruncates(t *testing.T) {
	// Compressed content stream: ~100 KB of text inside a ~1 KB member, so
	// the extraction cap (MaxMemberSize) trips while every declared-size
	// gate passes.
	pdf := writeFlatePDF(t, strings.Repeat("secret filler words ", 5000))
	assert.Less(t, len(pdf), 4096, "fixture must stay under the member size gate")
	limits := testMemberLimits
	limits.MaxMemberSize = 4096
	path := writeZipFixture(t, []zipMember{{"dense.pdf", pdf}})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", limits, nil)
	got := drainMembers(u)
	assert.Contains(t, got, "dense.pdf")
	assert.LessOrEqual(t, len(got["dense.pdf"]), 4096+4, "member text must stop at the cap (UTF-8 slack)")

	msgs := u.SkipMessages()
	assert.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "Stopped content scan of archive member: PDF exceeds")
}

func TestPDFMemberChargeAffectsNextMember(t *testing.T) {
	pdf := writeMinimalPDF("member secret text")
	bigText := bytes.Repeat([]byte("x"), len(pdf)+64)
	limits := testMemberLimits
	limits.MaxTotalMemory = int64(len(pdf) + 64)
	path := writeZipFixture(t, []zipMember{
		{"doc.pdf", pdf},
		{"big.txt", bigText},
	})
	u := InitArchiveIterator(context.Background(), path, "fixture.zip", limits, nil)
	got := drainMembers(u)
	assert.Contains(t, got, "doc.pdf")
	assert.NotContains(t, got, "big.txt", "charged PDF text must shrink the remaining budget")

	msgs := u.SkipMessages()
	assert.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "would exceed total archive memory limit")
}

// writeLyingZip stores members whose header over-declares the uncompressed
// size by one byte - extractors hand the recipient the complete file, so a
// silent skip here would be pure scan evasion.
func writeLyingZip(t *testing.T, members []zipMember) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		w, err := zw.CreateRaw(&zip.FileHeader{
			Name:               m.name,
			Method:             zip.Store,
			CRC32:              crc32.ChecksumIEEE(m.data),
			CompressedSize64:   uint64(len(m.data)),
			UncompressedSize64: uint64(len(m.data)) + 1,
		})
		assert.NoError(t, err)
		_, err = w.Write(m.data)
		assert.NoError(t, err)
	}
	assert.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "lying.zip")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return path
}

func TestOverDeclaredMembersStillScanned(t *testing.T) {
	xlsx, err := os.ReadFile("../../testdata/test.xlsx")
	assert.NoError(t, err)

	path := writeLyingZip(t, []zipMember{
		{"report.pdf", writeMinimalPDF("archived secret token here")},
		{"sheet.xlsx", xlsx},
		{"notes.txt", []byte("plain secret text\n")},
	})
	u := InitArchiveIterator(context.Background(), path, "lying.zip", testMemberLimits, nil)
	got := drainMembers(u)

	// Every member's real bytes are intact; over-declaring must not hide them.
	assert.Contains(t, string(got["report.pdf"]), "archived secret token")
	assert.Contains(t, strings.ToLower(string(got["sheet.xlsx"])), "column2")
	assert.Contains(t, string(got["notes.txt"]), "plain secret text")
}

// TestZeroPDFArchiveKeepsRuntimeLazy re-executes itself in a child process
// so the assertion runs in a process that has NEVER touched a PDF. Checking
// the flag in-process is worthless: any earlier test in the package has
// already initialized the pool, which silently turned the previous version
// of this test into a no-op.
func TestZeroPDFArchiveKeepsRuntimeLazy(t *testing.T) {
	if os.Getenv("PC_LAZY_PDF_CHILD") == "1" {
		path := writeZipFixture(t, []zipMember{
			{"readme.txt", []byte("plain text\n")},
			{"data.csv", []byte("a,b\n1,2\n")},
		})
		u := InitArchiveIterator(context.Background(), path, "fixture.zip", testMemberLimits, nil)
		got := drainMembers(u)
		assert.Len(t, got, 2)
		if pdfRuntimeInitialized() {
			t.Fatal("PDF-free archive iteration must not initialize the wasm runtime")
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestZeroPDFArchiveKeepsRuntimeLazy", "-test.v")
	cmd.Env = append(os.Environ(), "PC_LAZY_PDF_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child run failed: %v\n%s", err, out)
	}
}
