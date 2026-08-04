package readers

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

// testOOXMLLimits are generous limits for fixture tests.
var testOOXMLLimits = ArchiveLimits{
	MaxMemberSize:  100 * 1024 * 1024,
	MaxTotalMemory: 1024 * 1024 * 1024,
	MaxMemberCount: 1000,
}

func TestOOXMLGateRejectsDeclaredBomb(t *testing.T) {
	// Container with a huge declared entry and a corrupt tail: the gate must
	// reject on the index alone, before any decompression could hit the tail.
	var raw bytes.Buffer
	raw.WriteByte(0x00)
	assert.NoError(t, binary.Write(&raw, binary.LittleEndian, uint16(64)))
	assert.NoError(t, binary.Write(&raw, binary.LittleEndian, ^uint16(64)))
	raw.Write(make([]byte, 64))
	raw.Write([]byte{0x07, 0xff, 0xff, 0xff})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "xl/worksheets/sheet1.xml",
		Method:             zip.Deflate,
		UncompressedSize64: 1 << 40, // 1 TB declared
		CompressedSize64:   uint64(raw.Len()),
	})
	assert.NoError(t, err)
	_, err = w.Write(raw.Bytes())
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())

	data := bytes.NewReader(buf.Bytes())
	_, _, err = ReadXLSX(data, int64(buf.Len()), ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024})
	assert.True(t, errors.Is(err, ErrOOXMLDeclaredSize), "expected declared-size rejection, got %v", err)
}

func TestOOXMLGateSumLimit(t *testing.T) {
	// Entries individually under the member limit but summing past the total.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	payload := bytes.Repeat([]byte("a"), 600)
	for _, name := range []string{"a.xml", "b.xml", "c.xml"} {
		w, err := zw.Create(name)
		assert.NoError(t, err)
		_, err = w.Write(payload)
		assert.NoError(t, err)
	}
	assert.NoError(t, zw.Close())

	r := bytes.NewReader(buf.Bytes())
	_, err := ooxmlZipReader(r, int64(buf.Len()), ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 1500})
	assert.True(t, errors.Is(err, ErrOOXMLDeclaredSize))

	_, err = ooxmlZipReader(r, int64(buf.Len()), ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 4096})
	assert.NoError(t, err)
}

func TestOOXMLNotZipSentinel(t *testing.T) {
	data := []byte("just,a,csv\nmisnamed,as,xlsx\n")
	_, _, err := ReadXLSX(bytes.NewReader(data), int64(len(data)), testOOXMLLimits)
	assert.True(t, errors.Is(err, ErrOOXMLNotZip))
	_, _, err = ReadDOCX(bytes.NewReader(data), int64(len(data)), testOOXMLLimits)
	assert.True(t, errors.Is(err, ErrOOXMLNotZip))
}

// buildAmplifiedXLSX rebuilds the fixture with one 2 KB shared string
// referenced by 200 cells: every zip entry stays small (passes the gate) but
// the EXTRACTED text is ~400 KB - the shared-string amplification the text
// cap exists for.
func buildAmplifiedXLSX(t *testing.T) []byte {
	t.Helper()
	src, err := zip.OpenReader("../../testdata/test.xlsx")
	assert.NoError(t, err)
	defer src.Close()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range src.File {
		if f.Name == "xl/worksheets/sheet1.xml" || f.Name == "xl/sharedStrings.xml" {
			continue
		}
		w, err := zw.Create(f.Name)
		assert.NoError(t, err)
		rc, err := f.Open()
		assert.NoError(t, err)
		_, err = io.Copy(w, rc)
		rc.Close()
		assert.NoError(t, err)
	}

	big := strings.Repeat("A", 2048)
	w, err := zw.Create("xl/sharedStrings.xml")
	assert.NoError(t, err)
	_, err = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="1" uniqueCount="1"><si><t>` + big + `</t></si></sst>`))
	assert.NoError(t, err)

	var sheet bytes.Buffer
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d" t="s"><v>0</v></c></row>`, i, i)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	w, err = zw.Create("xl/worksheets/sheet1.xml")
	assert.NoError(t, err)
	_, err = w.Write(sheet.Bytes())
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestReadXLSXTruncationNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	data := buildAmplifiedXLSX(t)

	// Every entry passes the 16 KB member gate; extracted text (~400 KB)
	// busts the cap -> truncation with partial content and NO leaked
	// producer goroutine (drain pattern, not break).
	content, truncated, err := ReadXLSX(bytes.NewReader(data), int64(len(data)),
		ArchiveLimits{MaxMemberSize: 16 * 1024, MaxTotalMemory: 1024 * 1024 * 1024})
	assert.NoError(t, err)
	assert.True(t, truncated, "amplified fixture must truncate at the text cap")
	assert.NotEmpty(t, content, "partial content must still be returned")
	total := 0
	for _, c := range content {
		total += len(c)
	}
	assert.Greater(t, total, 0)
	assert.Less(t, total, 64*1024, "content must stop near the cap, not extract everything")

	// Producers exit by drain; allow brief scheduling before comparing.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before, "truncation must not leak the sheet producer goroutine")
}

func TestWrapperMatchesCore(t *testing.T) {
	file := structs.File{Path: "../../testdata/test.xlsx", Name: "test.xlsx"}
	viaWrapper, truncatedW, err := ReadXLSXFile(file, testOOXMLLimits)
	assert.NoError(t, err)
	assert.False(t, truncatedW)

	data, err := os.ReadFile(file.Path)
	assert.NoError(t, err)
	viaCore, truncatedC, err := ReadXLSX(bytes.NewReader(data), int64(len(data)), testOOXMLLimits)
	assert.NoError(t, err)
	assert.False(t, truncatedC)
	assert.Equal(t, viaWrapper, viaCore)
}

func TestOOXMLGateBombFixtureOnDisk(t *testing.T) {
	// Same bomb via the path wrapper: ack-relevant sentinel must survive the
	// wrapper layer.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "word/document.xml",
		Method:             zip.Deflate,
		UncompressedSize64: 1 << 40,
		CompressedSize64:   8,
	})
	assert.NoError(t, err)
	_, err = w.Write([]byte{0x07, 0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00})
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "bomb.docx")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	_, _, err = ReadDOCXFile(structs.File{Path: path, Name: "bomb.docx"}, ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024})
	assert.True(t, errors.Is(err, ErrOOXMLDeclaredSize))
}
