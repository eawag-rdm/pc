package readers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
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

// ArchiveLimits carries the effective per-archive unpacking limits (derive via
// config.GeneralConfig.ArchiveLimits). The iterator applies them verbatim and
// fails closed: a non-positive limit means nothing qualifies, never "unlimited".
type ArchiveLimits struct {
	MaxMemberSize  int64 // per-member size ceiling (bytes)
	MaxTotalMemory int64 // per-archive decompressed-content budget (bytes)
	MaxMemberCount int   // unpack-candidate ceiling (stored here; enforced from C4)
}

type UnpackedFileIterator struct {
	ArchivePath   string
	ArchiveName   string
	MaxMemberSize int64
	Whitelist     []string
	Blacklist     []string

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
	maxMemberCount     int // unenforced until C4 (member counting)
	processedFileCount int

	// skipMessages accumulates skip acknowledgements for archive members that were
	// not content-scanned (per-member size limit or total-memory limit). Callers
	// drain these via SkipMessages() and thread them into their returned []Message.
	skipMessages []structs.Message

	// sniffBuf is per-member scratch for content-type detection (iterator is
	// single-goroutine); avoids one allocation per member.
	sniffBuf [512]byte

	// walkCounter bounds decompressed bytes for tar.gz walks (nil otherwise).
	walkCounter *countingReader

	tarFile        *os.File
	tarReader      *tar.Reader
	gzipReader     *gzip.Reader
	zipReader      *zip.ReadCloser
	sevenZipReader *sevenzip.ReadCloser
}

// declaredSizeBudgetMultiple bounds per-archive decompression work relative to
// maxTotalMemory: the 7z declared-size gate and the tar.gz walk cap.
const declaredSizeBudgetMultiple = 4

var errWalkCapExceeded = errors.New("archive walk cap exceeded")

// countingReader counts decompressed bytes flowing out of the gzip layer and
// fails hard once count exceeds limit, so member drains and reads alike stop
// within one chunk.
type countingReader struct {
	r     io.Reader
	count int64
	limit int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.count > c.limit {
		return 0, errWalkCapExceeded
	}
	n, err := c.r.Read(p)
	c.count += int64(n)
	return n, err
}

func InitArchiveIterator(archivePath string, archiveName string, limits ArchiveLimits, whitelist []string, blacklist []string) *UnpackedFileIterator {
	return &UnpackedFileIterator{
		ArchivePath:        archivePath,
		ArchiveName:        archiveName,
		MaxMemberSize:      limits.MaxMemberSize,
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
		maxTotalMemory:     limits.MaxTotalMemory,
		maxMemberCount:     limits.MaxMemberCount,
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

// recordArchiveSkip appends a skip acknowledgement for the archive as a whole
// (declared-size gate, walk cap). Source is the archive File itself with an
// empty ArchiveName, matching the existing archive-level message convention.
func (u *UnpackedFileIterator) recordArchiveSkip(reason string) {
	var size int64
	if fi, err := os.Stat(u.ArchivePath); err == nil {
		size = fi.Size()
	}
	archive := structs.ToFileWithDisplay(u.ArchivePath, u.ArchiveName, u.ArchiveName, size, "", "")
	u.skipMessages = append(u.skipMessages, structs.Message{
		Content: reason,
		Source:  archive,
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
	return fmt.Sprintf("Skipped content scan of archive member: size (%d bytes) exceeds maximum archive member size (%d bytes).", size, u.MaxMemberSize)
}

// memberMemorySkipReason builds the reason string for an archive member skipped
// because scanning it would exceed the total archive memory budget.
func (u *UnpackedFileIterator) memberMemorySkipReason() string {
	return fmt.Sprintf("Skipped content scan of archive member: would exceed total archive memory limit (%d bytes).", u.maxTotalMemory)
}

// memberOverrunSkipReason: member stream produced more data than its header
// declared (defense in depth against archive-library bugs).
func (u *UnpackedFileIterator) memberOverrunSkipReason(declared int64) string {
	return fmt.Sprintf("Skipped content scan of archive member: content exceeds declared size (%d bytes).", declared)
}

// declaredSizeGateSkipReason: whole archive rejected because the summed
// declared uncompressed sizes exceed the decompression-work bound.
func (u *UnpackedFileIterator) declaredSizeGateSkipReason() string {
	return fmt.Sprintf("Skipped content scan of archive: total declared uncompressed size exceeds %dx the total archive memory limit (%d bytes).", declaredSizeBudgetMultiple, u.maxTotalMemory)
}

// walkCapSkipReason: tar.gz walk stopped after decompressing the cap.
func (u *UnpackedFileIterator) walkCapSkipReason() string {
	return fmt.Sprintf("Stopped content scan of archive: decompressed data exceeds %dx the total archive memory limit (%d bytes).", declaredSizeBudgetMultiple, u.maxTotalMemory)
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
	return u.bufferNextTar()
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
		u.walkCounter = &countingReader{r: gzipReader, limit: declaredSizeBudgetMultiple * u.maxTotalMemory}
		u.tarReader = tar.NewReader(u.walkCounter)
	}
	return u.bufferNextTar()
}

// bufferNextTar advances the tar stream until the next scannable text member
// is buffered, exactly once per member. Skipped members are discarded by the
// next tar.Next call (a Seek on plain tar files, bounded by the walk cap for
// tar.gz). Returns false when iteration ended (EOF, error, or walk cap).
func (u *UnpackedFileIterator) bufferNextTar() bool {
	for {
		header, err := u.tarReader.Next()
		if err != nil {
			if errors.Is(err, errWalkCapExceeded) {
				u.recordArchiveSkip(u.walkCapSkipReason())
			}
			u.iterationEnded = true
			return false
		}
		u.fileIndex++

		isFile := !(header.Typeflag == tar.TypeDir)
		isGreaterZero := header.Size > 0
		isBelowMaxSize := header.Size <= u.MaxMemberSize

		// Stop before decompressing a member that would bust the walk cap anyway.
		if u.walkCounter != nil && u.walkCounter.count+header.Size > u.walkCounter.limit {
			u.recordArchiveSkip(u.walkCapSkipReason())
			u.iterationEnded = true
			return false
		}

		// Check memory limits
		if !u.checkMemoryLimit(header.Size) {
			if isFile && isGreaterZero {
				u.recordSkip(header.Name, u.memberMemorySkipReason(), header.Size)
			}
			continue
		}

		// Acknowledge members skipped purely because they exceed the size limit.
		if isFile && isGreaterZero && !isBelowMaxSize {
			u.recordSkip(header.Name, u.memberSizeSkipReason(header.Size), header.Size)
		}

		if !(isFile && isGreaterZero && isBelowMaxSize) || !fileGoodToUnpack(u.Whitelist, u.Blacklist, header.Name) {
			continue
		}

		isText, content, err := u.isTarTextFileWithContent(header)
		if err != nil || !isText {
			continue
		}

		u.bufferedFilename = header.Name
		u.bufferedFileContent = content
		u.bufferedFileSize = len(content)
		u.updateMemoryUsage(len(content))
		return true
	}
}

// isTarTextFileWithContent classifies the current tar member from its first
// 512 bytes and reads the rest only for text members. Tar framing enforces
// header.Size, so the content is preallocated exactly.
func (u *UnpackedFileIterator) isTarTextFileWithContent(header *tar.Header) (bool, []byte, error) {
	n, err := io.ReadFull(u.tarReader, u.sniffBuf[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, nil, err
	}

	if n == 0 || !strings.HasPrefix(http.DetectContentType(u.sniffBuf[:n]), "text/") {
		// Not text: the next tar.Next call discards the remaining bytes.
		return false, nil, nil
	}

	content := make([]byte, header.Size)
	copy(content, u.sniffBuf[:n])
	if int64(n) < header.Size {
		if _, err := io.ReadFull(u.tarReader, content[n:]); err != nil {
			return false, nil, fmt.Errorf("error reading rest of text file: %w", err)
		}
	}
	return true, content, nil
}

// unpackTar promotes the buffered member to current and buffers the next one.
func unpackTar(u *UnpackedFileIterator) (bool, error) {
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

	u.bufferNextTar()
	return true, nil
}

// sniffThenRead classifies rc from its first 512 bytes and reads the rest only
// for text members, so non-text members cost at most 512 decompressed bytes.
// declared is the header-declared size, already validated against MaxSize and
// the memory budget; both archive libraries cap reads at it, so the content is
// preallocated exactly. overrun means the stream outgrew declared (defense in
// depth against library bugs).
func (u *UnpackedFileIterator) sniffThenRead(rc io.Reader, declared int64) (isText bool, content []byte, overrun bool, err error) {
	n, err := io.ReadFull(rc, u.sniffBuf[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, nil, false, err
	}
	if n == 0 || !strings.HasPrefix(http.DetectContentType(u.sniffBuf[:n]), "text/") {
		return false, nil, false, nil
	}
	if int64(n) > declared {
		return true, nil, true, nil
	}

	content = make([]byte, declared)
	copy(content, u.sniffBuf[:n])
	if int64(n) < declared {
		m, err := io.ReadFull(rc, content[n:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// Stream ended early (7z members may be shorter than declared).
			return true, content[:n+m], false, nil
		}
		if err != nil {
			return true, nil, false, err
		}
	}

	// Probe one byte past declared: detects lying streams and, for zip, drives
	// the reader to EOF so its CRC check still runs.
	var probe [1]byte
	m, err := rc.Read(probe[:])
	if m > 0 {
		return true, nil, true, nil
	}
	if err != nil && err != io.EOF {
		return true, nil, false, err
	}
	return true, content, false, nil
}

// bufferNextZip scans forward from fileIndex+1, decompresses the next scannable
// text member exactly once into the look-ahead buffer. Ends iteration when no
// further member qualifies.
func (u *UnpackedFileIterator) bufferNextZip() bool {
	files := u.zipReader.File
	// Fail closed on non-positive limits (a negative int64 would wrap huge as uint64).
	sizeOK := u.MaxMemberSize > 0
	maxSize := uint64(max(u.MaxMemberSize, 0))

	for i := u.fileIndex + 1; i < len(files); i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize64 > 0
		isBelowMaxSize := sizeOK && f.UncompressedSize64 <= maxSize

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

		rc, err := f.Open()
		if err != nil {
			continue
		}
		isText, content, overrun, err := u.sniffThenRead(rc, int64(f.UncompressedSize64))
		rc.Close()
		if err != nil || !isText {
			continue
		}
		if overrun {
			u.recordSkip(f.Name, u.memberOverrunSkipReason(int64(f.UncompressedSize64)), int64(f.UncompressedSize64))
			continue
		}

		u.fileIndex = i
		u.bufferedFilename = f.Name
		u.bufferedFileContent = content
		u.bufferedFileSize = len(content)
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
	// Fail closed on non-positive limits (a negative int64 would wrap huge as uint64).
	sizeOK := u.MaxMemberSize > 0
	maxSize := uint64(max(u.MaxMemberSize, 0))

	for i := u.fileIndex + 1; i < len(files); i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize > 0
		isBelowMaxSize := sizeOK && f.UncompressedSize <= maxSize

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

		rc, err := f.Open()
		if err != nil {
			continue
		}
		isText, content, overrun, err := u.sniffThenRead(rc, int64(f.UncompressedSize))
		rc.Close()
		if err != nil || !isText {
			continue
		}
		if overrun {
			u.recordSkip(f.Name, u.memberOverrunSkipReason(int64(f.UncompressedSize)), int64(f.UncompressedSize))
			continue
		}

		u.fileIndex = i
		u.bufferedFilename = f.Name
		u.bufferedFileContent = content
		u.bufferedFileSize = len(content)
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
	if !u.passes7zDeclaredSizeGate() {
		u.recordArchiveSkip(u.declaredSizeGateSkipReason())
		u.iterationEnded = true
		return false
	}
	return u.bufferNext7z()
}

// passes7zDeclaredSizeGate bounds solid-folder transit decompression: in 7z,
// skipped members still cost transit CPU for later members in the same folder,
// so the summed declared sizes are the honest bound on decompression work.
// O(entries) field reads, zero decompression; the running sum rejects early
// and saturates so crafted huge headers cannot wrap it.
func (u *UnpackedFileIterator) passes7zDeclaredSizeGate() bool {
	limit := uint64(declaredSizeBudgetMultiple) * uint64(u.maxTotalMemory)
	var sum uint64
	for i := range u.sevenZipReader.File {
		s := u.sevenZipReader.File[i].UncompressedSize
		if s == 0 {
			continue
		}
		sum += s
		if sum < s || sum > limit {
			return false
		}
	}
	return true
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
		u.tarFile = nil
	}
	if u.gzipReader != nil {
		u.gzipReader.Close()
		u.gzipReader = nil
	}
	if u.zipReader != nil {
		u.zipReader.Close()
		u.zipReader = nil
	}
	if u.sevenZipReader != nil {
		u.sevenZipReader.Close()
		u.sevenZipReader = nil
	}
	u.tarReader = nil
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

	var found bool
	// Handle .tar.gz separately since filepath.Ext only returns .gz
	if strings.HasSuffix(u.ArchiveName, ".tar.gz") {
		found = u.findFirstTarGz()
	} else {
		switch filepath.Ext(u.ArchiveName) {
		case ".zip":
			found = u.findFirstZip()
		case ".tar":
			found = u.findFirstTar()
		case ".7z":
			found = u.findFirst7z()
		default:
			output.GlobalLogger.FileWarning(u.ArchiveName, "Unsupported archive type '%s'", u.ArchiveName)
			u.iterationEnded = true
		}
	}
	// Consumers never call HasNext after a false here, so release fds now.
	if !found {
		u.close()
	}
	return found
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
