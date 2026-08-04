package readers

import (
	"bytes"
	"io"
	"os"
	"sync"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/thedatashed/xlsxreader"
)

// Buffer pool to reduce memory allocations
var bufferPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 8192)) // 8KB initial capacity
	},
}

// ReadXLSX extracts per-sheet text from an xlsx container. The declared-size
// gate runs before any parsing; extraction stops once the accumulated text
// exceeds limits.MaxMemberSize (truncated = true, the partial content is
// still returned for scanning).
//
// Residual risk, unfixable from outside the lib: xlsxreader trusts the
// sharedStrings count attribute when pre-allocating, before any of our
// bounds apply.
func ReadXLSX(r io.ReaderAt, size int64, limits ArchiveLimits) (content [][]byte, truncated bool, err error) {
	zr, err := ooxmlZipReader(r, size, limits)
	if err != nil {
		return nil, false, err
	}
	xl, err := xlsxreader.NewReaderZip(zr)
	if err != nil {
		return nil, false, err
	}
	// NOTE: *XlsxFile has no Close; each ReadRows spawns a producer goroutine
	// whose only exit is draining its channel to EOF. Never break out of the
	// row loop - on cap hit we stop APPENDING but keep DRAINING the current
	// sheet (bounded by the per-entry gate), and open no further sheets.
	textCap := limits.MaxMemberSize
	var total int64

	content = make([][]byte, 0, len(xl.Sheets))
	for _, sheet := range xl.Sheets {
		if truncated {
			break // sheet not yet opened: no goroutine to drain
		}
		sheetBuffer := bufferPool.Get().(*bytes.Buffer)
		sheetBuffer.Reset()

		for row := range xl.ReadRows(sheet) {
			if truncated {
				continue // drain so the producer goroutine exits
			}
			rowBuffer := bufferPool.Get().(*bytes.Buffer)
			rowBuffer.Reset()

			for _, cell := range row.Cells {
				if cell.Type == "string" && len(cell.Value) > 0 {
					rowBuffer.WriteString(cell.Value)
					rowBuffer.WriteByte(' ')
				}
			}

			if rowBuffer.Len() > 0 {
				rowBuffer.WriteByte('\n')
				sheetBuffer.Write(rowBuffer.Bytes())
				total += int64(rowBuffer.Len())
				if total > textCap {
					truncated = true
				}
			}

			bufferPool.Put(rowBuffer)
		}

		if sheetBuffer.Len() > 0 {
			// Copy BEFORE returning the buffer to the pool (aliasing).
			sheetContent := make([]byte, sheetBuffer.Len())
			copy(sheetContent, sheetBuffer.Bytes())
			content = append(content, sheetContent)
		}

		bufferPool.Put(sheetBuffer)
	}

	return content, truncated, nil
}

// ReadXLSXFile is the path-based wrapper around ReadXLSX; the *os.File keeps
// the container streaming from disk exactly as before. The truncated flag is
// forwarded (the dormant secret-scan consumer will want it on reactivation).
func ReadXLSXFile(file structs.File, limits ArchiveLimits) ([][]byte, bool, error) {
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	return ReadXLSX(f, fi.Size(), limits)
}
