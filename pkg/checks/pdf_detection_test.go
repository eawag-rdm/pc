package checks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// buildTestPDF mirrors the readers-package fixture builder: a valid PDF with
// one page per text, xref offsets recorded while writing.
func buildTestPDF(texts ...string) []byte {
	var buf bytes.Buffer
	offsets := []int{0}
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

func writePDFFixture(t *testing.T, data []byte) structs.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return structs.File{Path: path, Name: "doc.pdf"}
}

func TestKeywordDetectedInPDFWithPageNumber(t *testing.T) {
	file := writePDFFixture(t, buildTestPDF("clean first page", "the password lives here"))
	msgs := IsFreeOfKeywords(file, keywordConfig([]string{"password"}))
	found := false
	for _, m := range msgs {
		if !m.Skipped && strings.Contains(m.Content, "password") && strings.Contains(m.Content, "(page 2)") {
			found = true
		}
	}
	if !found {
		t.Errorf("keyword on page 2 must be found and cited with its page, got %v", msgs)
	}

	// Negative control.
	for _, m := range IsFreeOfKeywords(file, keywordConfig([]string{"zzz-not-present"})) {
		if !m.Skipped {
			t.Errorf("no finding expected for absent keyword, got %v", m)
		}
	}
}

func TestPDFPageCapStopsDetection(t *testing.T) {
	file := writePDFFixture(t, buildTestPDF("page one", "page two", "password on page three"))
	cfg := keywordConfig([]string{"password"})
	cfg.General.MaxPDFPages = 2

	msgs := IsFreeOfKeywords(file, cfg)
	foundKeyword, foundStop := false, false
	for _, m := range msgs {
		if !m.Skipped && strings.Contains(m.Content, "password") {
			foundKeyword = true
		}
		if m.Skipped && strings.Contains(m.Content, "Stopped content scan") {
			foundStop = true
		}
	}
	if foundKeyword {
		t.Errorf("keyword past the page cap must not be found, got %v", msgs)
	}
	if !foundStop {
		t.Errorf("expected truncation acknowledgement, got %v", msgs)
	}
}

func TestPDFJunkPrefixStillParsed(t *testing.T) {
	// PDFium accepts the magic anywhere in the first 1024 bytes; a junk
	// prefix must not evade scanning.
	data := append([]byte("JUNKJUNK"), buildTestPDF("hidden password here")...)
	file := writePDFFixture(t, data)
	msgs := IsFreeOfKeywords(file, keywordConfig([]string{"password"}))
	found := false
	for _, m := range msgs {
		if !m.Skipped && strings.Contains(m.Content, "password") {
			found = true
		}
	}
	if !found {
		t.Errorf("junk-prefixed PDF must still be scanned, got %v", msgs)
	}
}

func TestMisnamedTextPDFFallsBack(t *testing.T) {
	// No PDF magic at all: the generic text flow must keep scanning it.
	file := writePDFFixture(t, []byte("plain text with a password inside\n"))
	msgs := IsFreeOfKeywords(file, keywordConfig([]string{"password"}))
	found := false
	for _, m := range msgs {
		if !m.Skipped && strings.Contains(m.Content, "password") {
			found = true
		}
	}
	if !found {
		t.Errorf("text file misnamed .pdf must still be keyword-scanned, got %v", msgs)
	}
}

func TestUnreadablePDFEmitsSkipAck(t *testing.T) {
	file := writePDFFixture(t, buildTestPDF("some text"))
	if err := os.Chmod(file.Path, 0); err != nil {
		t.Fatal(err)
	}
	msgs := IsFreeOfKeywords(file, keywordConfig([]string{"password"}))
	found := false
	for _, m := range msgs {
		if m.Skipped && strings.Contains(m.Content, "could not be read") {
			found = true
		}
	}
	if !found {
		t.Errorf("unreadable PDF must produce a read skip ack, got %v", msgs)
	}
}

func TestMalformedPDFEmitsSkipAck(t *testing.T) {
	junk := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x13, 0x00, 0x42}, 2048)...)
	file := writePDFFixture(t, junk)
	msgs := IsFreeOfKeywords(file, keywordConfig([]string{"password"}))
	found := false
	for _, m := range msgs {
		if m.Skipped && strings.Contains(m.Content, "could not be parsed") {
			found = true
		}
	}
	if !found {
		t.Errorf("malformed PDF must produce a parse skip ack, got %v", msgs)
	}
}
