package readers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

type UnpackedFileIterator struct {
	ArchivePath string
	ArchiveName string
	MaxSize     int
	Whitelist   []string
	Blacklist   []string

	CurrentFilename    string
	CurrentFileContent []byte
	CurrentFileSize    int

	bufferedFilename    string
	bufferedFileContent []byte
	bufferedFileSize    int
	iterationEnded      bool
	hasCheckedFirstFile bool
	fileIndex           int

	// Memory tracking
	totalMemoryUsed    int64
	maxTotalMemory     int64
	processedFileCount int

	// skipMessages accumulates skip acknowledgements for archive members that were
	// not content-scanned (per-member size limit or total-memory limit). Callers
	// drain these via SkipMessages() and thread them into their returned []Message.
	skipMessages []structs.Message

	tarFile        *os.File
	tarReader      *tar.Reader
	gzipReader     *gzip.Reader
	zipReader      *zip.ReadCloser
	sevenZipReader *sevenzip.ReadCloser
}

func InitArchiveIterator(archivePath string, archiveName string, maxSize int, whitelist []string, blacklist []string, maxTotalMemory int64) *UnpackedFileIterator {
	return &UnpackedFileIterator{
		ArchivePath:        archivePath,
		ArchiveName:        archiveName,
		MaxSize:            maxSize,
		Whitelist:          whitelist,
		Blacklist:          blacklist,
		CurrentFilename:    "",
		CurrentFileContent: []byte{},
		CurrentFileSize:    0,

		bufferedFilename:    "",
		bufferedFileContent: []byte{},
		bufferedFileSize:    0,

		iterationEnded:      false,
		hasCheckedFirstFile: false,
		fileIndex:           -1,

		totalMemoryUsed:    0,
		maxTotalMemory:     maxTotalMemory,
		processedFileCount: 0,

		tarFile:        nil,
		tarReader:      nil,
		gzipReader:     nil,
		zipReader:      nil,
		sevenZipReader: nil,
	}
}

func (u *UnpackedFileIterator) UnpackedFile() (string, []byte, int) {
	return u.CurrentFilename, u.CurrentFileContent, u.CurrentFileSize
}

// checkMemoryLimit verifies if processing another file would exceed memory limits
func (u *UnpackedFileIterator) checkMemoryLimit(additionalBytes int64) bool {
	return u.totalMemoryUsed+additionalBytes <= u.maxTotalMemory
}

// updateMemoryUsage tracks memory usage and enforces limits
func (u *UnpackedFileIterator) updateMemoryUsage(fileSize int) {
	u.totalMemoryUsed += int64(fileSize)
	u.processedFileCount++

	// Log memory usage every 10 files
	if u.processedFileCount%10 == 0 {
		output.GlobalLogger.Info("Archive memory usage: %d/%d bytes (%d files processed)",
			u.totalMemoryUsed, u.maxTotalMemory, u.processedFileCount)
	}
}

// recordSkip appends a skip acknowledgement Message for an archive member that
// was not content-scanned. The member is represented as a structs.File whose
// ArchiveName points back to the containing archive so every output can attribute
// the skip correctly. The caller assigns TestName.
func (u *UnpackedFileIterator) recordSkip(memberName, reason string, memberSize int64) {
	member := structs.ToFileWithDisplay(
		u.ArchivePath, // path stays as the archive path (member has no standalone path)
		memberName,    // name is the path within the archive
		memberName,    // display name
		memberSize,    // size
		"",            // suffix (auto-detected)
		u.ArchiveName, // archive name reference
	)
	u.skipMessages = append(u.skipMessages, structs.Message{
		Content: reason,
		Source:  member,
		Skipped: true,
		Reason:  reason,
	})
}

// SkipMessages returns the skip acknowledgements collected so far for archive
// members that were not content-scanned (per-member size or total-memory limit).
func (u *UnpackedFileIterator) SkipMessages() []structs.Message {
	return u.skipMessages
}

// memberSizeSkipReason builds the reason string for an archive member skipped
// because it exceeds the per-member size limit.
func (u *UnpackedFileIterator) memberSizeSkipReason(size int64) string {
	return fmt.Sprintf("Skipped content scan of archive member: size (%d bytes) exceeds maximum archive member size (%d bytes).", size, u.MaxSize)
}

// memberMemorySkipReason builds the reason string for an archive member skipped
// because scanning it would exceed the total archive memory budget.
func (u *UnpackedFileIterator) memberMemorySkipReason() string {
	return fmt.Sprintf("Skipped content scan of archive member: would exceed total archive memory limit (%d bytes).", u.maxTotalMemory)
}

func matchLiteralPatterns(list []string, str string) bool {
	if len(list) == 0 || str == "" {
		return true // Empty patterns match everything
	}

	// Use fast matcher for pattern detection
	matcher := optimization.GetMatcher(list)
	return matcher.HasAnyMatch([]byte(str))
}

func fileGoodToUnpack(whitelist []string, blacklist []string, filename string) bool {
	if len(blacklist) > 0 {
		return !matchLiteralPatterns(blacklist, filename)
	}
	if len(whitelist) > 0 {
		return matchLiteralPatterns(whitelist, filename)
	}
	return true
}

func (u *UnpackedFileIterator) findFirstTar() bool {
	if u.tarReader == nil {
		file, err := os.Open(u.ArchivePath)
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Error (archive content checks) opening tar file '%s' -> %v", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.tarFile = file
		u.tarReader = tar.NewReader(file)
	}

	// Buffer the first valid file
	for {
		header, err := u.tarReader.Next()
		if err != nil {
			u.iterationEnded = true
			return false
		}
		u.fileIndex++

		isFile := !(header.Typeflag == tar.TypeDir)
		isGreaterZero := header.Size > 0
		isBelowMaxSize := header.Size <= int64(u.MaxSize)

		// Check memory limits
		if !u.checkMemoryLimit(header.Size) {
			if isFile && isGreaterZero {
				u.recordSkip(header.Name, u.memberMemorySkipReason(), header.Size)
			}
			// Skip remaining bytes
			_, _ = io.CopyN(io.Discard, u.tarReader, header.Size)
			continue
		}

		// Acknowledge members skipped purely because they exceed the size limit.
		if isFile && isGreaterZero && !isBelowMaxSize {
			u.recordSkip(header.Name, u.memberSizeSkipReason(header.Size), header.Size)
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, header.Name)
		}

		if isGoodToUnpack {
			isText, content, err := u.isTarTextFileWithContent(header, u.tarReader)
			if err != nil {
				continue
			}
			if !isText {
				continue
			}

			u.bufferedFilename = header.Name
			u.bufferedFileContent = content
			u.bufferedFileSize = len(content)
			u.updateMemoryUsage(len(content))
			return true
		} else {
			// Skip non-matching files
			_, _ = io.CopyN(io.Discard, u.tarReader, header.Size)
			continue
		}
	}
}

func (u *UnpackedFileIterator) findFirstTarGz() bool {
	if u.tarReader == nil {
		file, err := os.Open(u.ArchivePath)
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Error (archive content checks) opening tar.gz file '%s' -> %v", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.tarFile = file

		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Error (archive content checks) creating gzip reader for '%s' -> %v", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.gzipReader = gzipReader
		u.tarReader = tar.NewReader(gzipReader)
	}

	// Buffer the first valid file
	for {
		header, err := u.tarReader.Next()
		if err != nil {
			u.iterationEnded = true
			return false
		}
		u.fileIndex++

		isFile := !(header.Typeflag == tar.TypeDir)
		isGreaterZero := header.Size > 0
		isBelowMaxSize := header.Size <= int64(u.MaxSize)

		// Check memory limits
		if !u.checkMemoryLimit(header.Size) {
			if isFile && isGreaterZero {
				u.recordSkip(header.Name, u.memberMemorySkipReason(), header.Size)
			}
			// Skip remaining bytes
			_, _ = io.CopyN(io.Discard, u.tarReader, header.Size)
			continue
		}

		// Acknowledge members skipped purely because they exceed the size limit.
		if isFile && isGreaterZero && !isBelowMaxSize {
			u.recordSkip(header.Name, u.memberSizeSkipReason(header.Size), header.Size)
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, header.Name)
		}

		if isGoodToUnpack {
			isText, content, err := u.isTarTextFileWithContent(header, u.tarReader)
			if err != nil {
				continue
			}
			if !isText {
				continue
			}

			u.bufferedFilename = header.Name
			u.bufferedFileContent = content
			u.bufferedFileSize = len(content)
			u.updateMemoryUsage(len(content))
			return true
		} else {
			// Skip non-matching files
			_, _ = io.CopyN(io.Discard, u.tarReader, header.Size)
			continue
		}
	}
}

func (u *UnpackedFileIterator) isTarTextFileWithContent(header *tar.Header, reader io.Reader) (bool, []byte, error) {
	const sampleSize = 512
	buffer := make([]byte, sampleSize)

	n, err := reader.Read(buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, nil, err
	}

	if n == 0 || !strings.HasPrefix(http.DetectContentType(buffer[:n]), "text/") {
		// Not a text file: skip remaining bytes
		remaining := header.Size - int64(n)
		if remaining > 0 {
			_, _ = io.CopyN(io.Discard, reader, remaining)
		}
		return false, nil, nil
	}

	// Read rest of file content
	remaining := header.Size - int64(n)
	rest, err := io.ReadAll(io.LimitReader(reader, remaining))
	if err != nil {
		return false, nil, fmt.Errorf("error reading rest of text file: %w", err)
	}

	fullContent := append(buffer[:n], rest...)
	return true, fullContent, nil
}

func unpackTar(u *UnpackedFileIterator) (bool, error) {
	if u.iterationEnded {
		return false, nil
	}

	if u.bufferedFilename != "" {
		u.CurrentFilename = u.bufferedFilename
		u.CurrentFileContent = u.bufferedFileContent
		u.CurrentFileSize = u.bufferedFileSize

		u.bufferedFilename = ""
		u.bufferedFileContent = nil
		u.bufferedFileSize = 0
	} else {
		return false, nil
	}

	// Buffer next valid file
	for {
		header, err := u.tarReader.Next()
		if err == io.EOF {
			u.iterationEnded = true
			break
		}
		if err != nil {
			u.iterationEnded = true
			return true, fmt.Errorf("error reading tar header: %w", err)
		}
		u.fileIndex++

		isFile := !(header.Typeflag == tar.TypeDir)
		isGreaterZero := header.Size > 0
		isBelowMaxSize := header.Size <= int64(u.MaxSize)

		// Check memory limits
		if !u.checkMemoryLimit(header.Size) {
			if isFile && isGreaterZero {
				u.recordSkip(header.Name, u.memberMemorySkipReason(), header.Size)
			}
			// Skip remaining bytes
			_, _ = io.CopyN(io.Discard, u.tarReader, header.Size)
			continue
		}

		// Acknowledge members skipped purely because they exceed the size limit.
		if isFile && isGreaterZero && !isBelowMaxSize {
			u.recordSkip(header.Name, u.memberSizeSkipReason(header.Size), header.Size)
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, header.Name)
		}

		if isGoodToUnpack {
			isText, content, err := u.isTarTextFileWithContent(header, u.tarReader)
			if err != nil {
				u.iterationEnded = true
				return true, err
			}
			if !isText {
				continue
			}

			u.bufferedFilename = header.Name
			u.bufferedFileContent = content
			u.bufferedFileSize = len(content)
			u.updateMemoryUsage(len(content))
			break
		} else {
			// Skip non-matching files
			_, _ = io.CopyN(io.Discard, u.tarReader, header.Size)
			continue
		}
	}

	return true, nil
}

// is7zTextFileWithContent decompresses the member at index once and reports
// whether it is text.
func (u *UnpackedFileIterator) is7zTextFileWithContent(index int) (bool, []byte, error) {
	f := u.sevenZipReader.File[index]

	rc, err := f.Open()
	if err != nil {
		return false, nil, err
	}
	defer rc.Close()

	// Read the entire file content once
	content, err := io.ReadAll(rc)
	if err != nil {
		return false, nil, err
	}

	if len(content) == 0 {
		return false, nil, nil
	}

	// Use the first 512 bytes for content type detection
	sampleSize := 512
	if len(content) < sampleSize {
		sampleSize = len(content)
	}

	filetype := http.DetectContentType(content[:sampleSize])
	isText := strings.HasPrefix(filetype, "text/") // Same logic as TAR and ZIP

	return isText, content, nil
}

// isZippedTextWithContent decompresses the member at fileIndex once and reports
// whether it is text.
func (u *UnpackedFileIterator) isZippedTextWithContent(fileIndex int) (bool, []byte, error) {
	file := u.zipReader.File[fileIndex]

	rc, err := file.Open()
	if err != nil {
		return false, nil, err
	}
	defer rc.Close()

	// Read the entire file content once
	content, err := io.ReadAll(rc)
	if err != nil {
		return false, nil, err
	}

	if len(content) == 0 {
		return false, nil, nil
	}

	// Use the first 512 bytes for content type detection (same as original)
	sampleSize := 512
	if len(content) < sampleSize {
		sampleSize = len(content)
	}

	filetype := http.DetectContentType(content[:sampleSize])
	isText := strings.HasPrefix(filetype, "text/") // Same logic as TAR and 7Z

	return isText, content, nil
}

// bufferNextZip scans forward from fileIndex+1, decompresses the next scannable
// text member exactly once into the look-ahead buffer. Ends iteration when no
// further member qualifies.
func (u *UnpackedFileIterator) bufferNextZip() bool {
	files := u.zipReader.File
	maxSize := uint64(u.MaxSize)

	for i := u.fileIndex + 1; i < len(files); i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize64 > 0
		isBelowMaxSize := f.UncompressedSize64 <= maxSize

		// Check memory limits
		if !u.checkMemoryLimit(int64(f.UncompressedSize64)) {
			if isFile && isGreaterZero {
				u.recordSkip(f.Name, u.memberMemorySkipReason(), int64(f.UncompressedSize64))
			}
			continue
		}

		// Acknowledge members skipped purely because they exceed the size limit.
		if isFile && isGreaterZero && !isBelowMaxSize {
			u.recordSkip(f.Name, u.memberSizeSkipReason(int64(f.UncompressedSize64)), int64(f.UncompressedSize64))
		}

		if !(isFile && isGreaterZero && isBelowMaxSize) || !fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name) {
			continue
		}

		isText, content, err := u.isZippedTextWithContent(i)
		if err != nil || !isText {
			continue
		}

		u.fileIndex = i
		u.bufferedFilename = f.Name
		u.bufferedFileContent = content
		u.bufferedFileSize = int(f.UncompressedSize64)
		u.updateMemoryUsage(len(content))
		return true
	}

	u.iterationEnded = true
	return false
}

// unpackZip promotes the buffered member to current and buffers the next one,
// so each member is decompressed and charged to the memory budget exactly once.
func unpackZip(u *UnpackedFileIterator) (bool, error) {
	if u.bufferedFilename == "" {
		u.iterationEnded = true
		return false, nil
	}

	u.CurrentFilename = u.bufferedFilename
	u.CurrentFileContent = u.bufferedFileContent
	u.CurrentFileSize = u.bufferedFileSize
	u.bufferedFilename = ""
	u.bufferedFileContent = nil
	u.bufferedFileSize = 0

	u.bufferNextZip()
	return true, nil
}

// bufferNext7z scans forward from fileIndex+1, decompresses the next scannable
// text member exactly once into the look-ahead buffer. Ends iteration when no
// further member qualifies.
func (u *UnpackedFileIterator) bufferNext7z() bool {
	files := u.sevenZipReader.File
	maxSize := uint64(u.MaxSize)

	for i := u.fileIndex + 1; i < len(files); i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize > 0
		isBelowMaxSize := f.UncompressedSize <= maxSize

		// Check memory limits
		if !u.checkMemoryLimit(int64(f.UncompressedSize)) {
			if isFile && isGreaterZero {
				u.recordSkip(f.Name, u.memberMemorySkipReason(), int64(f.UncompressedSize))
			}
			continue
		}

		// Acknowledge members skipped purely because they exceed the size limit.
		if isFile && isGreaterZero && !isBelowMaxSize {
			u.recordSkip(f.Name, u.memberSizeSkipReason(int64(f.UncompressedSize)), int64(f.UncompressedSize))
		}

		if !(isFile && isGreaterZero && isBelowMaxSize) || !fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name) {
			continue
		}

		isText, content, err := u.is7zTextFileWithContent(i)
		if err != nil || !isText {
			continue
		}

		u.fileIndex = i
		u.bufferedFilename = f.Name
		u.bufferedFileContent = content
		u.bufferedFileSize = int(f.UncompressedSize)
		u.updateMemoryUsage(len(content))
		return true
	}

	u.iterationEnded = true
	return false
}

func (u *UnpackedFileIterator) findFirst7z() bool {
	if u.sevenZipReader == nil {
		reader, err := sevenzip.OpenReader(u.ArchivePath)
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Error (archive content checks) opening 7z file '%s' -> %v", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.sevenZipReader = reader
	}
	return u.bufferNext7z()
}

// unpack7z promotes the buffered member to current and buffers the next one,
// so each member is decompressed and charged to the memory budget exactly once.
func unpack7z(u *UnpackedFileIterator) (bool, error) {
	if u.bufferedFilename == "" {
		u.iterationEnded = true
		return false, nil
	}

	u.CurrentFilename = u.bufferedFilename
	u.CurrentFileContent = u.bufferedFileContent
	u.CurrentFileSize = u.bufferedFileSize
	u.bufferedFilename = ""
	u.bufferedFileContent = nil
	u.bufferedFileSize = 0

	u.bufferNext7z()
	return true, nil
}

func (u *UnpackedFileIterator) findFirstZip() bool {
	if u.zipReader == nil {
		reader, err := zip.OpenReader(u.ArchivePath)
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Error (archive content checks) opening zip file '%s' -> %v", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.zipReader = reader
	}
	return u.bufferNextZip()
}

func (u *UnpackedFileIterator) close() {
	if u.tarFile != nil {
		u.tarFile.Close()
	}
	if u.gzipReader != nil {
		u.gzipReader.Close()
	}
	if u.zipReader != nil {
		u.zipReader.Close()
	}
	if u.sevenZipReader != nil {
		u.sevenZipReader.Close()
	}
	if u.tarReader != nil {
		u.tarReader = nil
	}
}

func (u *UnpackedFileIterator) HasNext() bool {
	if u.iterationEnded {
		u.close()
	}
	return !u.iterationEnded
}

func (u *UnpackedFileIterator) HasFilesToUnpack() bool {

	if u.hasCheckedFirstFile {
		return !u.iterationEnded
	}
	u.hasCheckedFirstFile = true
	// Handle .tar.gz separately since filepath.Ext only returns .gz
	if strings.HasSuffix(u.ArchiveName, ".tar.gz") {
		return u.findFirstTarGz()
	}

	switch filepath.Ext(u.ArchiveName) {
	case ".zip":
		return u.findFirstZip()
	case ".tar":
		return u.findFirstTar()
	case ".7z":
		return u.findFirst7z()
	default:
		output.GlobalLogger.FileWarning(u.ArchiveName, "Unsupported archive type '%s'", u.ArchiveName)
		u.iterationEnded = true
		u.close()
		return false
	}
}

func (u *UnpackedFileIterator) Next() bool {
	if u.iterationEnded {
		return false
	}

	var ok bool
	var err error

	// Handle .tar.gz separately since filepath.Ext only returns .gz
	if strings.HasSuffix(u.ArchiveName, ".tar.gz") {
		ok, err = unpackTar(u) // Reuse TAR unpacking logic for TAR.GZ
	} else {
		switch filepath.Ext(u.ArchiveName) {
		case ".zip":
			ok, err = unpackZip(u)
		case ".tar":
			ok, err = unpackTar(u)
		case ".7z":
			ok, err = unpack7z(u)
		default:
			u.iterationEnded = true
			return false
		}
	}

	if err != nil {
		u.iterationEnded = true
		u.close()
		return false
	}

	return ok // true only if valid file was found
}
