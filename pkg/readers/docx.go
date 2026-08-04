package readers

import (
	"io"
	"os"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/fumiama/go-docx"
)

// ReadDOCX extracts per-paragraph/table text from a docx container. The
// declared-size gate runs before parsing and is the real memory bound here:
// go-docx fully materializes the document DOM (and media) before iteration,
// so the text cap only trims the returned content.
func ReadDOCX(r io.ReaderAt, size int64, limits ArchiveLimits) (content [][]byte, truncated bool, err error) {
	if _, err := ooxmlZipReader(r, size, limits); err != nil {
		return nil, false, err
	}
	doc, err := docx.Parse(r, size)
	if err != nil {
		return nil, false, err
	}
	if doc == nil {
		return [][]byte{}, false, nil
	}

	textCap := limits.MaxMemberSize
	var total int64
	content = [][]byte{}
	for _, it := range doc.Document.Body.Items {
		var block []byte
		switch v := it.(type) {
		case *docx.Paragraph:
			block = []byte(v.String())
		case *docx.Table:
			block = []byte(v.String())
		default:
			continue
		}
		if total+int64(len(block)) > textCap {
			truncated = true
			break
		}
		content = append(content, block)
		total += int64(len(block))
	}

	return content, truncated, nil
}

// ReadDOCXFile is the path-based wrapper around ReadDOCX; the *os.File keeps
// the container streaming from disk exactly as before. The truncated flag is
// forwarded (the dormant secret-scan consumer will want it on reactivation).
func ReadDOCXFile(file structs.File, limits ArchiveLimits) ([][]byte, bool, error) {
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	return ReadDOCX(f, fi.Size(), limits)
}
