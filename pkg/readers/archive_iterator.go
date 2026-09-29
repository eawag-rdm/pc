package readers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// ArchiveLimits carries the effective per-archive unpacking limits (derive via
// config.GeneralConfig.ArchiveLimits). The iterator applies them verbatim and
// fails closed: a non-positive limit means nothing qualifies, never "unlimited".
type ArchiveLimits struct {
	MaxMemberSize  int64 // per-member size ceiling (bytes)
	MaxTotalMemory int64 // per-archive decompressed-content budget (bytes)
	MaxMemberCount int   // unpack-candidate ceiling (stored here; enforced from C4)
	MaxPDFPages    int   // page ceiling per PDF member; over it the member is skipped whole (0 = PDF members skipped)
	MaxPDFFileSize int64 // PDF member admission gate (bytes); over it the member is not read
}

type UnpackedFileIterator struct {
	ArchivePath   string
	ArchiveName   string
	MaxMemberSize int64

	// MemberNames is the collector the caller copies over from the archive
	// File's hand-off field, so the member names this walk already sees do not
	// have to be decompressed a second time for the name checks. Only the
	// tar.gz walk fills it; nil - the hot path - collects nothing.
	//
	// Assign it before the first HasFilesToUnpack/HasNext or nothing is
	// collected.
	MemberNames *structs.ArchiveNameCollector

	// ctx bounds a member's PDF extraction. Stored rather than threaded
	// because one iterator serves one request, so it outlives nothing.
	ctx context.Context

	// memberFilter decides which members are content-scanned; nil filters
	// nothing. It is compiled once by the caller and shared read-only across
	// archives and goroutines. memberScratch carries one member name through it
	// and is NOT shareable: one iterator runs on one goroutine (like sniffBuf
	// below), so the scratch lives here. memberBaseName selects the base-name
	// subject, read once at construction instead of per member - forward
	// support for rule-derived selectors, which no production caller builds
	// yet: every filter reaching this iterator today declares subject "path".
	memberFilter   *selector.Selector
	memberScratch  selector.Scratch
	memberBaseName bool

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
	maxMemberCount     int
	processedFileCount int

	// PDF member extraction: page limit (0 = fail closed, skip PDF members)
	// and the per-archive wall-clock budget that bounds crafted many-PDF
	// archives (the per-member timeout alone would amplify to hours).
	maxPDFPages      int
	maxPDFFileSize   int64
	pdfWallTime      time.Duration
	pdfBudgetAckSent bool

	// candidateCount tracks unpack candidates (regular, size > 0, name-filter
	// pass) for the tar family, where counting is only possible inline during
	// the single pass. zip/7z pre-count over their indexes instead.
	candidateCount int

	// skipMessages accumulates skip acknowledgements for archive members that were
	// not content-scanned (per-member size limit or total-memory limit). Callers
	// drain these via SkipMessages() and thread them into their returned []Message.
	skipMessages []structs.Message

	// sniffBuf is per-member scratch for content-type detection (iterator is
	// single-goroutine); avoids one allocation per member. sniffLen is how
	// much of it the last sniff filled, so callers can reuse those bytes
	// instead of re-reading them.
	sniffBuf [512]byte
	sniffLen int

	// walkCounter bounds decompressed bytes for tar.gz walks (nil otherwise).
	walkCounter *countingReader

	// fillMemberNames is true between a claimed MemberNames fill and the Finish
	// that closes it. The member loop tests this one bool, so a walk with no
	// collector - and a walk whose fill was refused - costs what it costs today.
	fillMemberNames bool

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

// InitArchiveIterator prepares an iterator over the scannable members of one
// archive. memberFilter decides which members are admitted for content
// scanning; nil is the single form of "unfiltered", and a selector holding no
// pattern is normalized to it here, so the member loop never asks a filter that
// cannot reject anything. Compile the filter once and share it - it is
// stateless, and the per-member scratch lives on the iterator.
//
// ctx must be non-nil: it bounds PDF member extraction and ends the iteration
// at the first member it cancels, while the member walk at large is the
// caller's to stop.
func InitArchiveIterator(ctx context.Context, archivePath string, archiveName string, limits ArchiveLimits, memberFilter *selector.Selector) *UnpackedFileIterator {
	if memberFilter != nil && memberFilter.Unfiltered() {
		memberFilter = nil
	}
	baseName := memberFilter != nil && memberFilter.Subject() == selector.SubjectName
	return &UnpackedFileIterator{
		ArchivePath:        archivePath,
		ArchiveName:        archiveName,
		MaxMemberSize:      limits.MaxMemberSize,
		ctx:                ctx,
		memberFilter:       memberFilter,
		memberBaseName:     baseName,
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
		maxPDFPages:        limits.MaxPDFPages,
		maxPDFFileSize:     limits.MaxPDFFileSize,
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

// updateMemoryUsage charges a scanned member against the archive memory
// budget and counts it. Enforcement is checkMemoryLimit's job, not this
// one's; processedFileCount has no production reader and exists for the
// "each member is processed exactly once" assertions in the tests.
func (u *UnpackedFileIterator) updateMemoryUsage(fileSize int) {
	u.totalMemoryUsed += int64(fileSize)
	u.processedFileCount++
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
	// From the constructed Name, not from memberName: a nameless member (a
	// crafted archive can hold one) has its Name filled with the archive's base
	// name by ToFileWithDisplay, and RelPath must carry that same string rather
	// than stay empty.
	member.RelPath = member.Name
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

// memberOpenSkipReason: the archive library could not open the member.
func (u *UnpackedFileIterator) memberOpenSkipReason() string {
	return "Skipped content scan of archive member: member could not be read."
}

// memberCountSkipReason: the archive holds more unpack candidates than the
// configured limit - "scannable", because filtered-out entries never count. The
// message names the maximum and not the actual count: for the tar family the
// exact total is unknowable without decompressing the whole stream.
func (u *UnpackedFileIterator) memberCountSkipReason() string {
	return fmt.Sprintf("Skipped content scan of archive: scannable member count exceeds maximum (%d).", u.maxMemberCount)
}

// admitMember reports whether a member survives the name filter, the first and
// cheapest of the three admission gates (name -> size -> memory).
//
// The subject is the member path VERBATIM - no normalization, so a directory
// member keeps the trailing slash its archive gave it - or the member's base
// name when the filter declares subject "name", which for a directory member is
// the last path element (path.Base("sub/dir/") == "dir"). The scratch folds the
// subject at most once however many patterns read it. The admitted set is
// pinned by the frozen table in TestFiltersDuringArchiveIteration
// (archive_iterator_test.go).
//
// Hot path: one call per member per walk, no allocation and no lock.
func (u *UnpackedFileIterator) admitMember(memberPath string) bool {
	if u.memberFilter == nil {
		return true
	}
	subject := memberPath
	if u.memberBaseName {
		subject = path.Base(memberPath)
	}
	u.memberScratch.Set(subject)
	return u.memberFilter.MatchScratch(&u.memberScratch)
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
		tarGzStreamOpens.Add(1)
		u.walkCounter = &countingReader{r: gzipReader, limit: walkByteBudget(u.maxTotalMemory)}
		u.tarReader = tar.NewReader(u.walkCounter)
		if u.MemberNames != nil {
			u.fillMemberNames = u.MemberNames.BeginFill()
		}
	}
	return u.bufferNextTar()
}

// noteMemberName hands one tar header to the claimed MemberNames fill. The File
// is built exactly as ReadTarGzFileListWithDisplayName builds it, so the fused
// walk and the standalone file list hand the name checks identical input -
// except for ArchiveName, which is the archive's Name here and its DISPLAY name
// there, and which dispatch rewrites on the collected members before running any
// check over them.
func (u *UnpackedFileIterator) noteMemberName(header *tar.Header) {
	member := structs.ToFileWithDisplay(u.ArchivePath, header.Name, header.Name, header.Size, "", u.ArchiveName)
	member.RelPath = member.Name
	u.MemberNames.Note(member)
}

// endMemberNames finishes a claimed MemberNames fill, exactly once per walk. It
// is reached from the end of the archive alone; every other exit leaves a
// partial list, which stays unusable precisely because it is never finished.
func (u *UnpackedFileIterator) endMemberNames() {
	if !u.fillMemberNames {
		return
	}
	u.fillMemberNames = false
	u.MemberNames.Finish()
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
			if errors.Is(err, io.EOF) {
				u.endMemberNames()
			}
			u.iterationEnded = true
			return false
		}

		// Stop before decompressing a member that would bust the walk cap anyway.
		// This stays FIRST: cap enforcement must not slide into the drains.
		// Subtraction, not addition: a PAX header size of MaxInt64 wraps the sum
		// negative and would slip past the stop.
		if u.walkCounter != nil && header.Size > u.walkCounter.limit-u.walkCounter.count {
			u.recordArchiveSkip(u.walkCapSkipReason())
			u.iterationEnded = true
			return false
		}

		// Every header, before any admission gate: the name checks see the
		// directories and the zero-size members too.
		if u.fillMemberNames {
			u.noteMemberName(header)
		}

		// Ack precedence: name filter (silent) -> size -> memory. Members the
		// check excludes by name get no acknowledgements at all.
		isFile := !(header.Typeflag == tar.TypeDir)
		if !(isFile && header.Size > 0) || !u.admitMember(header.Name) {
			continue
		}
		// Candidates count toward the member limit whether or not they end up
		// size- or memory-skipped; members already yielded stay scanned.
		u.candidateCount++
		if u.candidateCount > u.maxMemberCount {
			u.recordArchiveSkip(u.memberCountSkipReason())
			u.iterationEnded = true
			return false
		}
		if header.Size > u.MaxMemberSize {
			u.recordSkip(header.Name, u.memberSizeSkipReason(header.Size), header.Size)
			continue
		}
		if !u.checkMemoryLimit(header.Size) {
			u.recordSkip(header.Name, u.memberMemorySkipReason(), header.Size)
			continue
		}

		// Truncated members yield their truncated content (tar errors are
		// sticky, so iteration ends on the next Next call either way).
		if u.tryBufferMember(header.Name, header.Size, u.tarReader) {
			return true
		}
		// The member handler ends the iteration when the caller gave up; walking
		// on would decompress and buffer the next qualifying member for nobody.
		if u.iterationEnded {
			return false
		}
	}
}

// tryBufferMember runs one candidate member through the shared classify ->
// read -> ack-or-buffer -> charge tail. It fills the look-ahead buffer and
// returns true when the member qualified. Read errors and non-text members
// are skipped silently (the caller's loop continues); overruns get an ack.
// OOXML and PDF members route by extension BEFORE the sniff (they classify
// as binary).
func (u *UnpackedFileIterator) tryBufferMember(name string, declared int64, r io.Reader) bool {
	if kind := OOXMLKind(name); kind != "" {
		return u.tryBufferOOXMLMember(name, declared, r, kind)
	}
	if strings.EqualFold(filepath.Ext(name), ".pdf") {
		return u.tryBufferPDFMember(name, declared, r, nil, true)
	}
	isText, content, overrun, err := u.sniffThenRead(r, declared)
	if err != nil {
		return false
	}
	if !isText {
		// Content-routed PDF: a genuine PDF whose name lacks the .pdf
		// extension (stripped, or "report.pdf " with a trailing space that
		// extractors normalize away) classifies as binary and would be
		// dropped silently - the extension is the attacker's to choose.
		// The sniff bytes are reused, so this costs nothing extra; members
		// that then fail to parse stay silent exactly like binaries today.
		if prefix := u.sniffBuf[:u.sniffLen]; bytes.Contains(prefix, PDFMagic) {
			return u.tryBufferPDFMember(name, declared, r, prefix, false)
		}
		return false
	}
	if overrun {
		u.recordSkip(name, u.memberOverrunSkipReason(declared), declared)
		return false
	}
	u.bufferedFilename = name
	u.bufferedFileContent = content
	u.bufferedFileSize = len(content)
	u.updateMemoryUsage(len(content))
	return true
}

// readDeclaredMember reads up to declared bytes, optionally after a prefix
// already consumed from r, and returns what actually arrived. A member that
// delivers LESS than its header declares is common in 7z and is also a
// scan-evasion vector in zip (a one-byte over-declaration would otherwise
// make the member vanish silently while extractors still hand the recipient
// the complete file), so short delivery yields the partial content instead
// of discarding it - the same stance sniffThenRead takes for text members.
// ok is false only for a hard read error.
func readDeclaredMember(r io.Reader, declared int64, prefix []byte) ([]byte, bool) {
	data := make([]byte, declared)
	copy(data, prefix)
	if int64(len(prefix)) >= declared {
		return data, true
	}
	n, err := io.ReadFull(r, data[len(prefix):])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false
	}
	return data[:len(prefix)+n], true
}

// tryBufferOOXMLMember extracts the text of an xlsx/docx archive member and
// buffers it as ONE concatenated block (per-sheet indexing is lost for
// members - documented trade-off). Extracted text is what gets charged to the
// archive memory budget; the extraction cap is the remaining budget, so
// shared-string amplification cannot blow past it.
func (u *UnpackedFileIterator) tryBufferOOXMLMember(name string, declared int64, r io.Reader, kind string) bool {
	data, ok := readDeclaredMember(r, declared, nil)
	if !ok {
		return false // unreadable container member
	}
	declared = int64(len(data)) // header may have over-declared; use what arrived

	limits := ArchiveLimits{
		MaxMemberSize:  min(u.MaxMemberSize, u.maxTotalMemory-u.totalMemoryUsed),
		MaxTotalMemory: u.maxTotalMemory,
	}
	var content [][]byte
	var truncated bool
	var err error
	if kind == "xlsx" {
		content, truncated, err = ReadXLSX(bytes.NewReader(data), declared, limits)
	} else {
		content, truncated, err = ReadDOCX(bytes.NewReader(data), declared, limits)
	}
	if errors.Is(err, ErrOOXMLNotZip) {
		// Misnamed member: classify the raw bytes like the sniff path would.
		n := min(len(data), len(u.sniffBuf))
		if n == 0 || !strings.HasPrefix(http.DetectContentType(data[:n]), "text/") {
			return false
		}
		u.bufferedFilename = name
		u.bufferedFileContent = data
		u.bufferedFileSize = len(data)
		u.updateMemoryUsage(len(data))
		return true
	}
	if errors.Is(err, ErrOOXMLDeclaredSize) {
		u.recordSkip(name, fmt.Sprintf("Skipped content scan of archive member: container declares more data than allowed (member limit %d bytes, total limit %d bytes).", limits.MaxMemberSize, limits.MaxTotalMemory), declared)
		return false
	}
	if err != nil {
		output.GlobalLogger.FileWarning(u.ArchiveName, "Cannot parse %s member '%s' -> %v", kind, name, err)
		u.recordSkip(name, "Skipped content scan of archive member: container could not be parsed.", declared)
		return false
	}

	text := bytes.Join(content, []byte("\n"))
	if len(text) == 0 {
		return false
	}
	if truncated {
		u.recordSkip(name, fmt.Sprintf("Stopped content scan of archive member: extracted text exceeds %d bytes; scanned the first part only.", limits.MaxMemberSize), declared)
	}
	u.bufferedFilename = name
	u.bufferedFileContent = text
	u.bufferedFileSize = len(text)
	u.updateMemoryUsage(len(text))
	return true
}

// tryBufferPDFMember extracts the text of a PDF archive member and buffers it
// as ONE concatenated block (page attribution is lost for members - same
// documented trade-off as OOXML sheet indexing). The magic is sniffed from a
// small prefix BEFORE committing to the full member read, so a binary member
// merely named .pdf costs at most PDFMagicWindow decompressed bytes, like the
// generic sniff path. Extracted text is charged to the archive budget and the
// extraction cap is the remaining budget; a per-archive wall-clock budget
// (maxArchivePDFTime) bounds crafted many-PDF archives.
//
// consumed carries bytes already read from r (the caller's sniff). byName is
// true when the .pdf extension routed the member here: content-routed members
// that turn out not to parse stay silent, exactly like the binary members
// they would otherwise have been.
func (u *UnpackedFileIterator) tryBufferPDFMember(name string, declared int64, r io.Reader, consumed []byte, byName bool) bool {
	if u.maxPDFPages <= 0 || u.maxPDFFileSize <= 0 {
		return false // fail closed, silently: iterator built without PDF limits
	}
	// Declared size is known from the header, so an over-limit PDF member is
	// turned away before any of it is decompressed.
	if declared > u.maxPDFFileSize {
		if byName {
			u.recordSkip(name, fmt.Sprintf("Skipped content scan of archive member: PDF exceeds the maximum PDF size (%d bytes).", u.maxPDFFileSize), declared)
		}
		return false
	}

	prefix := make([]byte, min(declared, PDFMagicWindow))
	copy(prefix, consumed)
	if int64(len(consumed)) < int64(len(prefix)) {
		pn, err := io.ReadFull(r, prefix[len(consumed):])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false // unreadable container member
		}
		// Short delivery still gets scanned (see readDeclaredMember).
		prefix = prefix[:len(consumed)+pn]
	}
	readRest := func() []byte {
		data, ok := readDeclaredMember(r, declared, prefix)
		if !ok {
			return nil
		}
		return data
	}

	if !bytes.Contains(prefix, PDFMagic) {
		// Misnamed member: classify the prefix like the sniff path would.
		n := min(len(prefix), len(u.sniffBuf))
		if n == 0 || !strings.HasPrefix(http.DetectContentType(prefix[:n]), "text/") {
			return false // binary: bail without decompressing the rest
		}
		data := readRest()
		if data == nil {
			return false
		}
		u.bufferedFilename = name
		u.bufferedFileContent = data
		u.bufferedFileSize = len(data)
		u.updateMemoryUsage(len(data))
		return true
	}

	// Genuine PDF: the wall-clock budget only gates extraction, never the
	// misnamed-text fallback above. One archive-level ack, then silence.
	remainingBudget := maxArchivePDFTime - u.pdfWallTime
	if remainingBudget <= 0 {
		if !u.pdfBudgetAckSent {
			u.pdfBudgetAckSent = true
			u.recordArchiveSkip(fmt.Sprintf("Stopped PDF extraction for archive: cumulative PDF extraction time exceeds %s; remaining PDF members not scanned.", maxArchivePDFTime))
		}
		return false
	}

	// A caller that has given up must not pay for the rest of this member, nor
	// for the PDF members behind it: the walk ends here, silently - an ack
	// would blame a readable document for the cancellation.
	if u.ctx.Err() != nil {
		u.iterationEnded = true
		return false
	}

	data := readRest()
	if data == nil {
		return false
	}
	limits := PDFLimits{
		MaxFileBytes: u.maxPDFFileSize,
		MaxPages:     u.maxPDFPages,
		MaxTextBytes: min(u.MaxMemberSize, u.maxTotalMemory-u.totalMemoryUsed),
		// Clamped to what is left, so maxArchivePDFTime is a real ceiling
		// rather than a floor the last member can overshoot by a full
		// DefaultPDFTimeout.
		Timeout: min(DefaultPDFTimeout, remainingBudget),
	}
	// Charge extraction only: pool queue time belongs to whoever held the
	// worker, and charging it here would let a busy pool silently consume
	// this archive's scan budget. A crash retry's worker wait is the one
	// exception, bounded like a timed-out document: its timeout plus the grace.
	pageBlocks, truncated, extractTime, err := readPDF(u.ctx, data, limits)
	u.pdfWallTime += extractTime

	switch {
	case errors.Is(err, ErrPDFPassword):
		u.recordSkip(name, "Skipped content scan of archive member: PDF is password-protected.", declared)
		return false
	case errors.Is(err, ErrPDFTimeout):
		u.recordSkip(name, "Skipped content scan of archive member: PDF extraction timed out.", declared)
		return false
	case errors.Is(err, ErrPDFWorkerCrashed):
		u.recordSkip(name, "Skipped content scan of archive member: PDF worker crashed on the document.", declared)
		return false
	case errors.Is(err, ErrPDFTooLarge):
		u.recordSkip(name, fmt.Sprintf("Skipped content scan of archive member: PDF exceeds the maximum PDF size (%d bytes).", u.maxPDFFileSize), declared)
		return false
	case errors.Is(err, ErrPDFTooManyPages):
		u.recordSkip(name, fmt.Sprintf("Skipped content scan of archive member: PDF exceeds the maximum page count (%d).", u.maxPDFPages), declared)
		return false
	case errors.Is(err, ErrPDFRuntime):
		// One honest archive-level cause beats one bogus per-member parse
		// failure for every PDF in the archive.
		if !u.pdfBudgetAckSent {
			u.pdfBudgetAckSent = true
			u.recordArchiveSkip("Stopped PDF extraction for archive: PDF engine unavailable; PDF members not scanned.")
			u.skipMessages[len(u.skipMessages)-1].Transient = true
		}
		return false
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// The scan was abandoned, not the member: silent, because an ack
		// would blame a readable document for the caller giving up.
		return false
	case err != nil:
		if !byName {
			return false // content-routed non-PDF: silent, like any binary member
		}
		output.GlobalLogger.FileWarning(u.ArchiveName, "Cannot parse PDF member '%s' -> %v", name, err)
		u.recordSkip(name, "Skipped content scan of archive member: PDF could not be parsed.", declared)
		return false
	}

	// Drop empty placeholder pages before joining: an n-page image-only PDF
	// would otherwise yield n-1 newline bytes and dodge the empty check.
	nonEmpty := pageBlocks[:0]
	for _, p := range pageBlocks {
		if len(p) > 0 {
			nonEmpty = append(nonEmpty, p)
		}
	}
	text := bytes.Join(nonEmpty, []byte("\n"))
	if int64(len(text)) > limits.MaxTextBytes {
		// The join separators are not part of the extracted text ReadPDF
		// capped, so without this the charge could exceed the remaining
		// budget by one byte per page.
		text = text[:limits.MaxTextBytes]
		truncated = true
	}
	if len(text) == 0 {
		// Scanned/image-only PDF: the highest-risk shape (secrets live in the
		// image), so it must not read as "scanned and clean".
		u.recordSkip(name, "Skipped content scan of archive member: PDF contains no extractable text.", declared)
		return false
	}
	if truncated {
		u.recordSkip(name, fmt.Sprintf("Stopped content scan of archive member: PDF exceeds %d pages or %d extracted bytes; scanned the first part only.", limits.MaxPages, limits.MaxTextBytes), declared)
	}
	u.bufferedFilename = name
	u.bufferedFileContent = text
	u.bufferedFileSize = len(text)
	u.updateMemoryUsage(len(text))
	return true
}

// unpackTar promotes the buffered member to current and buffers the next one.
func unpackTar(u *UnpackedFileIterator) bool {
	if u.bufferedFilename == "" {
		u.iterationEnded = true
		return false
	}

	u.CurrentFilename = u.bufferedFilename
	u.CurrentFileContent = u.bufferedFileContent
	u.CurrentFileSize = u.bufferedFileSize
	u.bufferedFilename = ""
	u.bufferedFileContent = nil
	u.bufferedFileSize = 0

	u.bufferNextTar()
	return true
}

// sniffThenRead classifies rc from its first 512 bytes and reads the rest only
// for text members, so non-text members cost at most 512 decompressed bytes.
// declared is the header-declared size, already validated against MaxSize and
// the memory budget; both archive libraries cap reads at it, so the content is
// preallocated exactly. overrun means the stream outgrew declared (defense in
// depth against library bugs).
func (u *UnpackedFileIterator) sniffThenRead(rc io.Reader, declared int64) (isText bool, content []byte, overrun bool, err error) {
	n, err := io.ReadFull(rc, u.sniffBuf[:])
	u.sniffLen = n
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
			// A much-shorter member would pin the declared-size backing array
			// for its whole buffer lifetime: copy the small actual content out
			// and release the big allocation (also makes len == cap, so the
			// memory charge is honest).
			read := n + m
			if int64(cap(content))-int64(read) >= 64*1024 {
				trimmed := make([]byte, read)
				copy(trimmed, content[:read])
				return true, trimmed, false, nil
			}
			return true, content[:read], false, nil
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

		// Ack precedence: name filter (silent) -> size -> memory. Members the
		// check excludes by name get no acknowledgements at all.
		if f.FileInfo().IsDir() || f.UncompressedSize64 == 0 || !u.admitMember(f.Name) {
			continue
		}
		if !(sizeOK && f.UncompressedSize64 <= maxSize) {
			u.recordSkip(f.Name, u.memberSizeSkipReason(int64(f.UncompressedSize64)), int64(f.UncompressedSize64))
			continue
		}
		if !u.checkMemoryLimit(int64(f.UncompressedSize64)) {
			u.recordSkip(f.Name, u.memberMemorySkipReason(), int64(f.UncompressedSize64))
			continue
		}

		rc, err := f.Open()
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Cannot open archive member '%s' -> %v", f.Name, err)
			u.recordSkip(f.Name, u.memberOpenSkipReason(), int64(f.UncompressedSize64))
			continue
		}
		ok := u.tryBufferMember(f.Name, int64(f.UncompressedSize64), rc)
		rc.Close()
		if ok {
			u.fileIndex = i
			return true
		}
		// The member handler ends the iteration when the caller gave up; walking
		// on would decompress and buffer the next qualifying member for nobody.
		if u.iterationEnded {
			return false
		}
	}

	u.iterationEnded = true
	return false
}

// unpackZip promotes the buffered member to current and buffers the next one,
// so each member is decompressed and charged to the memory budget exactly once.
func unpackZip(u *UnpackedFileIterator) bool {
	if u.bufferedFilename == "" {
		u.iterationEnded = true
		return false
	}

	u.CurrentFilename = u.bufferedFilename
	u.CurrentFileContent = u.bufferedFileContent
	u.CurrentFileSize = u.bufferedFileSize
	u.bufferedFilename = ""
	u.bufferedFileContent = nil
	u.bufferedFileSize = 0

	u.bufferNextZip()
	return true
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

		// Ack precedence: name filter (silent) -> size -> memory. Members the
		// check excludes by name get no acknowledgements at all.
		if f.FileInfo().IsDir() || f.UncompressedSize == 0 || !u.admitMember(f.Name) {
			continue
		}
		if !(sizeOK && f.UncompressedSize <= maxSize) {
			u.recordSkip(f.Name, u.memberSizeSkipReason(int64(f.UncompressedSize)), int64(f.UncompressedSize))
			continue
		}
		if !u.checkMemoryLimit(int64(f.UncompressedSize)) {
			u.recordSkip(f.Name, u.memberMemorySkipReason(), int64(f.UncompressedSize))
			continue
		}

		rc, err := f.Open()
		if err != nil {
			output.GlobalLogger.FileWarning(u.ArchiveName, "Cannot open archive member '%s' -> %v", f.Name, err)
			u.recordSkip(f.Name, u.memberOpenSkipReason(), int64(f.UncompressedSize))
			continue
		}
		ok := u.tryBufferMember(f.Name, int64(f.UncompressedSize), rc)
		rc.Close()
		if ok {
			u.fileIndex = i
			return true
		}
		// The member handler ends the iteration when the caller gave up; walking
		// on would decompress and buffer the next qualifying member for nobody.
		if u.iterationEnded {
			return false
		}
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
	if u.sevenZipCandidateCountExceeded() {
		u.recordArchiveSkip(u.memberCountSkipReason())
		u.iterationEnded = true
		return false
	}
	return u.bufferNext7z()
}

// sevenZipCandidateCountExceeded mirrors zipCandidateCountExceeded over the
// 7z file list, trailing-slash exclusion included.
func (u *UnpackedFileIterator) sevenZipCandidateCountExceeded() bool {
	count := 0
	for i := range u.sevenZipReader.File {
		f := u.sevenZipReader.File[i]
		if f.UncompressedSize == 0 || strings.HasSuffix(f.Name, "/") || !u.admitMember(f.Name) {
			continue
		}
		count++
		if count > u.maxMemberCount {
			return true
		}
	}
	return false
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
func unpack7z(u *UnpackedFileIterator) bool {
	if u.bufferedFilename == "" {
		u.iterationEnded = true
		return false
	}

	u.CurrentFilename = u.bufferedFilename
	u.CurrentFileContent = u.bufferedFileContent
	u.CurrentFileSize = u.bufferedFileSize
	u.bufferedFilename = ""
	u.bufferedFileContent = nil
	u.bufferedFileSize = 0

	u.bufferNext7z()
	return true
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
	if u.zipCandidateCountExceeded() {
		u.recordArchiveSkip(u.memberCountSkipReason())
		u.iterationEnded = true
		return false
	}
	return u.bufferNextZip()
}

// zipCandidateCountExceeded counts unpack candidates over the central
// directory: field reads plus the name filter, zero decompression, early exit
// past the limit. Directory entries carry size 0 and are excluded by the size
// term (no FileInfo call - it allocates); a crafted entry that claims a size
// AND a trailing slash is excluded by the slash, so this preview admits exactly
// what bufferNextZip's IsDir test admits.
func (u *UnpackedFileIterator) zipCandidateCountExceeded() bool {
	count := 0
	for i := range u.zipReader.File {
		f := u.zipReader.File[i]
		if f.UncompressedSize64 == 0 || strings.HasSuffix(f.Name, "/") || !u.admitMember(f.Name) {
			continue
		}
		count++
		if count > u.maxMemberCount {
			return true
		}
	}
	return false
}

// Close releases the underlying archive handles and ends iteration.
// Idempotent. Called automatically when iteration runs to its end; exported so
// consumers that exit early (error paths, future context cancellation) can
// release file descriptors deterministically instead of waiting for GC.
func (u *UnpackedFileIterator) Close() {
	u.iterationEnded = true
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
		u.Close()
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
		u.Close()
	}
	return found
}

func (u *UnpackedFileIterator) Next() bool {
	if u.iterationEnded {
		return false
	}

	// Handle .tar.gz separately since filepath.Ext only returns .gz
	if strings.HasSuffix(u.ArchiveName, ".tar.gz") {
		return unpackTar(u) // Reuse TAR unpacking logic for TAR.GZ
	}
	switch filepath.Ext(u.ArchiveName) {
	case ".zip":
		return unpackZip(u)
	case ".tar":
		return unpackTar(u)
	case ".7z":
		return unpack7z(u)
	default:
		u.iterationEnded = true
		return false
	}
}
