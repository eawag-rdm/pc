package readers

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
)

// ErrOOXMLDeclaredSize rejects a container whose zip index declares more data
// than the archive limits allow (checked before any decompression).
var ErrOOXMLDeclaredSize = errors.New("ooxml container declares too much data")

// ErrOOXMLNotZip marks input that does not open as a zip container at all
// (e.g. a text file misnamed .xlsx); callers fall back to their generic flow.
var ErrOOXMLNotZip = errors.New("not a zip container")

// ooxmlZipReader opens the container and enforces the declared-size gate:
// no single entry may declare more than MaxMemberSize and the summed declared
// sizes must stay within MaxTotalMemory (XML compresses 5-10x, so the member
// knob alone would false-reject ordinary documents). Field reads only, zero
// decompression; sound because archive/zip hard-stops at declared sizes.
func ooxmlZipReader(r io.ReaderAt, size int64, limits ArchiveLimits) (*zip.Reader, error) {
	zr, err := zip.NewReader(io.NewSectionReader(r, 0, size), size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOOXMLNotZip, err)
	}
	var sum uint64
	for i := range zr.File {
		declared := zr.File[i].UncompressedSize64
		if declared == 0 {
			continue
		}
		if limits.MaxMemberSize <= 0 || declared > uint64(limits.MaxMemberSize) {
			return nil, fmt.Errorf("%w: entry %q declares %d bytes (member limit %d)", ErrOOXMLDeclaredSize, zr.File[i].Name, declared, limits.MaxMemberSize)
		}
		sum += declared
		if sum < declared || limits.MaxTotalMemory <= 0 || sum > uint64(limits.MaxTotalMemory) {
			return nil, fmt.Errorf("%w: declared total exceeds %d bytes", ErrOOXMLDeclaredSize, limits.MaxTotalMemory)
		}
	}
	return zr, nil
}
