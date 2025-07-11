package readers

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/eawag-rdm/pc/pkg/performance"
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

	tarFile        *os.File
	tarReader      *tar.Reader
	zipReader      *zip.ReadCloser
	sevenZipReader *sevenzip.ReadCloser
}

func InitArchiveIterator(archivePath string, archiveName string, maxSize int, whitelist []string, blacklist []string) *UnpackedFileIterator {
	return InitArchiveIteratorWithMemoryLimit(archivePath, archiveName, maxSize, whitelist, blacklist, 100*1024*1024) // Default 100MB
}

func InitArchiveIteratorWithMemoryLimit(archivePath string, archiveName string, maxSize int, whitelist []string, blacklist []string, maxTotalMemory int64) *UnpackedFileIterator {
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
		fmt.Printf("Archive memory usage: %d/%d bytes (%d files processed)\n", 
			u.totalMemoryUsed, u.maxTotalMemory, u.processedFileCount)
	}
}

func matchPatterns(list []string, str string) bool {
	if len(list) == 0 || str == "" {
		return true // Empty patterns match everything
	}
	
	// Use fast matcher for pattern detection
	matcher := performance.GetMatcher(list)
	return matcher.HasAnyMatch([]byte(str))
}

func fileGoodToUnpack(whitelist []string, blacklist []string, filename string) bool {
	if len(blacklist) > 0 {
		return !matchPatterns(blacklist, filename)
	}
	if len(whitelist) > 0 {
		return matchPatterns(whitelist, filename)
	}
	return true
}





func (u *UnpackedFileIterator) findFirstTar() bool {
	if u.tarReader == nil {
		file, err := os.Open(u.ArchivePath)
		if err != nil {
			fmt.Printf("Error (archive content checks) opening tar file '%s' -> %v\n", u.ArchiveName, err)
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
		isBelowMaxSize := header.Size < int64(u.MaxSize)

		isGoodToUnpack := false
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, header.Name)
		} else {
			continue
		}
		if !isGoodToUnpack {
			continue
		}

		isText, content, err := u.isTarTextFileWithContent(header, u.tarReader)
		if err != nil {
			u.iterationEnded = true
			return false
		}
		if !isText {
			continue
		}

		u.bufferedFilename = header.Name
		u.bufferedFileContent = content
		u.bufferedFileSize = len(content)
		return true
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
		isBelowMaxSize := header.Size < int64(u.MaxSize)

		isGoodToUnpack := false
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, header.Name)
		} else {
			continue
		}
		if !isGoodToUnpack {
			continue
		}

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
		break
	}

	return true, nil
}





// Optimized 7z file processing that eliminates double reading
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

// Optimized ZIP file processing that eliminates double reading (same pattern as TAR)
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

// Optimized ZIP unpacking that uses single-read approach
func unpackZip(u *UnpackedFileIterator) (bool, error) {
	files := u.zipReader.File
	length := len(files)
	maxSize := uint64(u.MaxSize)

	if length == 0 || u.iterationEnded || u.fileIndex >= length {
		u.iterationEnded = true
		return false, nil
	}

	// Use the optimized single-read approach
	isText, content, err := u.isZippedTextWithContent(u.fileIndex)
	if err != nil {
		u.iterationEnded = true
		return false, fmt.Errorf("error unpacking zip file: %w", err)
	}

	if !isText {
		// File is not text, try to find next valid file
		found := false
		for i := u.fileIndex + 1; i < length; i++ {
			f := files[i]
			isFile := !f.FileInfo().IsDir()
			isGreaterZero := f.UncompressedSize64 > 0
			isBelowMaxSize := f.UncompressedSize64 <= maxSize

			if !u.checkMemoryLimit(int64(f.UncompressedSize64)) {
				continue
			}

			var isGoodToUnpack bool
			if isFile && isGreaterZero && isBelowMaxSize {
				isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name)
			}
			
			if isGoodToUnpack {
				isText, content, err := u.isZippedTextWithContent(i)
				if err != nil {
					continue
				}
				if isText {
					u.fileIndex = i
					u.CurrentFilename = f.Name
					u.CurrentFileContent = content
					u.CurrentFileSize = int(f.UncompressedSize64)
					u.updateMemoryUsage(len(content))
					found = true
					break
				}
			}
		}
		
		if !found {
			u.iterationEnded = true
			return false, nil
		}
	} else {
		// Current file is valid, set it as current
		f := files[u.fileIndex]
		u.CurrentFilename = f.Name
		u.CurrentFileContent = content
		u.CurrentFileSize = int(f.UncompressedSize64)
		u.updateMemoryUsage(len(content))
	}

	// Look ahead for next file and buffer it (similar to original logic)
	found := false
	for i := u.fileIndex + 1; i < length; i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize64 > 0
		isBelowMaxSize := f.UncompressedSize64 <= maxSize

		if !u.checkMemoryLimit(int64(f.UncompressedSize64)) {
			continue
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name)
		}
		
		if isGoodToUnpack {
			isText, content, err := u.isZippedTextWithContent(i)
			if err != nil {
				continue
			}
			if isText {
				u.fileIndex = i
				u.bufferedFilename = f.Name
				u.bufferedFileContent = content
				u.bufferedFileSize = int(f.UncompressedSize64)
				u.updateMemoryUsage(len(content))
				found = true
				break
			}
		}
	}
	
	if !found {
		u.iterationEnded = true
	}

	return true, nil
}

func (u *UnpackedFileIterator) findFirst7z() bool {
	if u.sevenZipReader == nil {
		reader, err := sevenzip.OpenReader(u.ArchivePath)
		if err != nil {
			fmt.Printf("Error (archive content checks) opening 7z file '%s' -> %v\n", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.sevenZipReader = reader
	}
	
	files := u.sevenZipReader.File
	maxSize := uint64(u.MaxSize)
	
	startIndex := u.fileIndex
	if startIndex < 0 {
		startIndex = 0
	}
	
	for i := startIndex; i < len(files); i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize > 0
		isBelowMaxSize := f.UncompressedSize <= maxSize

		// Check memory limits
		if !u.checkMemoryLimit(int64(f.UncompressedSize)) {
			fmt.Printf("Skipping file %s: would exceed memory limit\n", f.Name)
			continue
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, files[i].Name)
		}
		
		if isGoodToUnpack {
			// Use optimized function that reads content only once
			isText, content, err := u.is7zTextFileWithContent(i)
			if err != nil {
				continue // Skip files that can't be read
			}
			if isText {
				u.fileIndex = i
				u.CurrentFilename = f.Name
				u.CurrentFileContent = content
				u.CurrentFileSize = int(f.UncompressedSize)
				u.updateMemoryUsage(len(content))
				return true
			}
		}
	}
	
	u.iterationEnded = true
	return false
}

func unpack7z(u *UnpackedFileIterator) (bool, error) {
	files := u.sevenZipReader.File
	length := len(files)
	maxSize := uint64(u.MaxSize)

	if length == 0 || u.iterationEnded || u.fileIndex >= length {
		u.iterationEnded = true
		return false, nil
	}

	// Use the optimized single-read approach
	isText, content, err := u.is7zTextFileWithContent(u.fileIndex)
	if err != nil {
		u.iterationEnded = true
		return false, fmt.Errorf("error unpacking 7z file: %w", err)
	}

	if !isText {
		// File is not text, try to find next valid file
		found := false
		for i := u.fileIndex + 1; i < length; i++ {
			f := files[i]
			isFile := !f.FileInfo().IsDir()
			isGreaterZero := f.UncompressedSize > 0
			isBelowMaxSize := f.UncompressedSize <= maxSize

			if !u.checkMemoryLimit(int64(f.UncompressedSize)) {
				continue
			}

			var isGoodToUnpack bool
			if isFile && isGreaterZero && isBelowMaxSize {
				isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name)
			}
			
			if isGoodToUnpack {
				isText, content, err := u.is7zTextFileWithContent(i)
				if err != nil {
					continue
				}
				if isText {
					u.fileIndex = i
					u.CurrentFilename = f.Name
					u.CurrentFileContent = content
					u.CurrentFileSize = int(f.UncompressedSize)
					u.updateMemoryUsage(len(content))
					found = true
					break
				}
			}
		}
		
		if !found {
			u.iterationEnded = true
			return false, nil
		}
	} else {
		// Current file is valid, set it as current
		f := files[u.fileIndex]
		u.CurrentFilename = f.Name
		u.CurrentFileContent = content
		u.CurrentFileSize = int(f.UncompressedSize)
		u.updateMemoryUsage(len(content))
	}

	// Look ahead for next file and buffer it (similar to original logic)
	found := false
	for i := u.fileIndex + 1; i < length; i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize > 0
		isBelowMaxSize := f.UncompressedSize <= maxSize

		if !u.checkMemoryLimit(int64(f.UncompressedSize)) {
			continue
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name)
		}
		
		if isGoodToUnpack {
			isText, content, err := u.is7zTextFileWithContent(i)
			if err != nil {
				continue
			}
			if isText {
				u.fileIndex = i
				u.bufferedFilename = f.Name
				u.bufferedFileContent = content
				u.bufferedFileSize = int(f.UncompressedSize)
				u.updateMemoryUsage(len(content))
				found = true
				break
			}
		}
	}
	
	if !found {
		u.iterationEnded = true
	}

	return true, nil
}

// Optimized ZIP findFirst method
func (u *UnpackedFileIterator) findFirstZip() bool {
	if u.zipReader == nil {
		reader, err := zip.OpenReader(u.ArchivePath)
		if err != nil {
			fmt.Printf("Error (archive content checks) opening zip file '%s' -> %v\n", u.ArchiveName, err)
			u.iterationEnded = true
			return false
		}
		u.zipReader = reader
	}
	
	files := u.zipReader.File
	maxSize := uint64(u.MaxSize)
	
	startIndex := u.fileIndex
	if startIndex < 0 {
		startIndex = 0
	}
	
	for i := startIndex; i < len(files); i++ {
		f := files[i]
		isFile := !f.FileInfo().IsDir()
		isGreaterZero := f.UncompressedSize64 > 0
		isBelowMaxSize := f.UncompressedSize64 <= maxSize

		// Check memory limits
		if !u.checkMemoryLimit(int64(f.UncompressedSize64)) {
			fmt.Printf("Skipping file %s: would exceed memory limit\n", f.Name)
			continue
		}

		var isGoodToUnpack bool
		if isFile && isGreaterZero && isBelowMaxSize {
			isGoodToUnpack = fileGoodToUnpack(u.Whitelist, u.Blacklist, f.Name)
		}
		
		if isGoodToUnpack {
			// Use optimized function that reads content only once
			isText, content, err := u.isZippedTextWithContent(i)
			if err != nil {
				continue // Skip files that can't be read
			}
			if isText {
				u.fileIndex = i
				u.CurrentFilename = f.Name
				u.CurrentFileContent = content
				u.CurrentFileSize = int(f.UncompressedSize64)
				u.updateMemoryUsage(len(content))
				return true
			}
		}
	}
	
	u.iterationEnded = true
	return false
}

func (u *UnpackedFileIterator) close() {
	if u.tarFile != nil {
		u.tarFile.Close()
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
	switch filepath.Ext(u.ArchiveName) {
	case ".zip":
		return u.findFirstZip()
	case ".tar":
		return u.findFirstTar()
	case ".7z":
		return u.findFirst7z()
	default:
		fmt.Printf("Unsupported archive type '%s'\n", u.ArchiveName)
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

	if err != nil {
		u.iterationEnded = true
		u.close()
		return false
	}

	return ok // true only if valid file was found
}
