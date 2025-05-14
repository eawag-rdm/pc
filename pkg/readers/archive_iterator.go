package readers

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/eawag-rdm/pc/pkg/structs"
)

type UnpackedFileIterator struct {
	Archive   string
	MaxSize   int
	Whitelist []string
	Blacklist []string

	CurrentFilename    string
	CurrentFileContent []byte
	CurrentFileSize    int

	bufferedFilename    string
	bufferedFileContent []byte
	bufferedFileSize    int
	iterationEnded      bool
	hasCheckedFirstFile bool
	fileIndex           int

	tarFile        *os.File
	tarReader      *tar.Reader
	zipReader      *zip.ReadCloser
	sevenZipReader *sevenzip.ReadCloser
}

// newUnpackedFileIterator is for testing purposes only, paths can be passes as strings
func newUnpackedFileIterator(archiveName string, maxSize int, whitelist []string, blacklist []string) *UnpackedFileIterator {
	return &UnpackedFileIterator{
		Archive:            archiveName,
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

		tarFile:        nil,
		tarReader:      nil,
		zipReader:      nil,
		sevenZipReader: nil,
	}
}

func InitArchiveIterator(archive structs.File, maxSize int, whitelist []string, blacklist []string) *UnpackedFileIterator {
	var arvicePath string
	if strings.HasSuffix(archive.Path, archive.Name) {
		arvicePath = archive.Path
	} else {
		arvicePath = filepath.Join(archive.Path, archive.Name)
	}
	return &UnpackedFileIterator{
		Archive:   arvicePath,
		MaxSize:   maxSize,
		Whitelist: whitelist,
		Blacklist: blacklist,

		CurrentFilename:    "",
		CurrentFileContent: []byte{},
		CurrentFileSize:    0,

		bufferedFilename:    "",
		bufferedFileContent: []byte{},
		bufferedFileSize:    0,

		iterationEnded:      false,
		hasCheckedFirstFile: false,
		fileIndex:           -1,

		tarFile:        nil,
		tarReader:      nil,
		zipReader:      nil,
		sevenZipReader: nil,
	}
}

func (u *UnpackedFileIterator) UnpackedFile() (string, []byte, int) {
	return u.CurrentFilename, u.CurrentFileContent, u.CurrentFileSize
}

func matchPatterns(list []string, str string) bool {
	combinedPattern := strings.Join(list, "|")
	combinedRegex := regexp.MustCompile(combinedPattern)
	return combinedRegex.MatchString(str)

}

func fileGoodToUnpack(blacklist []string, whitelist []string, filename string) bool {
	if len(blacklist) > 0 {
		return !matchPatterns(blacklist, filename)
	}
	if len(whitelist) > 0 {
		return matchPatterns(whitelist, filename)
	}
	return true
}

func fileContentsShouldBeUnpacked(isFile bool, isBelowMaxSize bool, fileGoodToUnpack bool) bool {
	return isFile && isBelowMaxSize && fileGoodToUnpack
}

func (u *UnpackedFileIterator) findFirstZip() bool {
	if u.zipReader == nil {
		reader, err := zip.OpenReader(u.Archive)
		if err != nil {
			u.iterationEnded = true
			return false
		}
		u.zipReader = reader
	}
	files := u.zipReader.File
	for i, f := range files {
		fileGoodToUnpack := fileGoodToUnpack(u.Blacklist, u.Whitelist, f.Name)
		isFile := !f.FileInfo().IsDir()
		isBelowMaxSize := f.UncompressedSize64 <= uint64(u.MaxSize)
		if fileContentsShouldBeUnpacked(isFile, isBelowMaxSize, fileGoodToUnpack) {
			if u.isZippedTextWithContent(i) {
				u.fileIndex = i

				name, content, size, err := u.unpackZipFile(i)
				if err != nil {
					continue
				}

				u.bufferedFilename = name
				u.bufferedFileContent = content
				u.bufferedFileSize = size

				return true
			}
		}
	}
	u.iterationEnded = true
	return false
}

// isTextFile checks if a file is a text file using DetectContentType from the http package.
func (u *UnpackedFileIterator) isZippedTextWithContent(fileIndex int) bool {
	file := u.zipReader.File[fileIndex]

	rc, err := file.Open()
	if err != nil {
		return false
	}
	defer rc.Close()

	// Read a small sample of the file
	const sampleSize = 512
	buffer := make([]byte, sampleSize)
	n, err := rc.Read(buffer)
	if err != nil && err.Error() != "EOF" {
		return false
	}

	if n == 0 {
		// Empty file or nothing to analyze
		return false
	}

	filetype := http.DetectContentType(buffer[:n])
	return strings.HasPrefix(filetype, "text/")
}

func (u *UnpackedFileIterator) unpackZipFile(fileIndex int) (string, []byte, int, error) {
	file := u.zipReader.File[fileIndex]

	rc, err := file.Open()
	if err != nil {
		return "", nil, 0, fmt.Errorf("error opening file:  %w", err)
	}
	defer rc.Close()
	content, err := io.ReadAll(rc)
	if err != nil {
		return "", nil, 0, fmt.Errorf("error reading file: %w", err)
	}

	return file.Name, content, int(file.UncompressedSize64), nil
}

func unpackZip(u *UnpackedFileIterator) (bool, error) {
	files := u.zipReader.File
	length := len(files)
	maxSize := uint64(u.MaxSize)

	if length == 0 || u.iterationEnded || u.fileIndex >= length {
		u.iterationEnded = true
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

		// Load current file
		name, content, size, err := u.unpackZipFile(u.fileIndex)
		if err != nil {
			u.iterationEnded = true
			return false, fmt.Errorf("error unpacking zip file: %w", err)
		}
		u.CurrentFilename = name
		u.CurrentFileContent = content
		u.CurrentFileSize = size
	}
	// Buffer next valid file
	found := false
	for i := u.fileIndex + 1; i < length; i++ {

		fileGoodToUnpack := fileGoodToUnpack(u.Blacklist, u.Whitelist, files[i].Name)
		isFile := !files[i].FileInfo().IsDir()
		isBelowMaxSize := files[i].UncompressedSize64 <= maxSize

		if fileContentsShouldBeUnpacked(isFile, isBelowMaxSize, fileGoodToUnpack) {
			if u.isZippedTextWithContent(i) {

				u.fileIndex = i
				name, content, size, err := u.unpackZipFile(i)
				if err != nil {
					u.iterationEnded = true
					continue
				}
				u.bufferedFilename = name
				u.bufferedFileContent = content
				u.bufferedFileSize = size
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

func (u *UnpackedFileIterator) findFirstTar() bool {
	if u.tarReader == nil {
		file, err := os.Open(u.Archive)
		if err != nil {
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

		if header.Typeflag == tar.TypeDir || header.Size > int64(u.MaxSize) {
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

func (u *UnpackedFileIterator) unpackTarFile(header *tar.Header, reader io.Reader) (string, []byte, int, error) {
	content := make([]byte, header.Size)
	if _, err := io.ReadFull(reader, content); err != nil {
		return "", nil, 0, fmt.Errorf("error reading tar file content: %w", err)
	}
	return header.Name, content, int(header.Size), nil
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

		if header.Typeflag == tar.TypeDir || header.Size > int64(u.MaxSize) {
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

func (u *UnpackedFileIterator) findFirst7z() bool {
	if u.sevenZipReader == nil {
		reader, err := sevenzip.OpenReader(u.Archive)
		if err != nil {
			u.iterationEnded = true
			return false
		}
		u.sevenZipReader = reader
	}
	files := u.sevenZipReader.File
	for i, f := range files {
		fileGoodToUnpack := fileGoodToUnpack(u.Blacklist, u.Whitelist, f.Name)
		isFile := !f.FileInfo().IsDir()
		isBelowMaxSize := f.UncompressedSize <= uint64(u.MaxSize)
		if fileContentsShouldBeUnpacked(isFile, isBelowMaxSize, fileGoodToUnpack) {
			if u.is7zTextFileWithContent(i) {
				u.fileIndex = i
				return true
			}
		}
	}
	u.iterationEnded = true
	return false
}

func (u *UnpackedFileIterator) is7zTextFileWithContent(index int) bool {
	f := u.sevenZipReader.File[index]

	rc, err := f.Open()
	if err != nil {
		return false
	}
	defer rc.Close()

	const sampleSize = 512
	buffer := make([]byte, sampleSize)
	n, err := rc.Read(buffer)
	if err != nil && err != io.EOF {
		return false
	}
	if n == 0 {
		// Empty file or nothing to analyze
		return false
	}

	filetype := http.DetectContentType(buffer[:n])
	return strings.HasPrefix(filetype, "text/")
}

func (u *UnpackedFileIterator) unpack7zFile(index int) (string, []byte, int, error) {
	f := u.sevenZipReader.File[index]
	rc, err := f.Open()
	if err != nil {
		return "", nil, 0, fmt.Errorf("error opening 7z file entry: %w", err)
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		return "", nil, 0, fmt.Errorf("error reading 7z file entry: %w", err)
	}

	return f.Name, content, int(f.UncompressedSize), nil
}

func unpack7z(u *UnpackedFileIterator) (bool, error) {
	files := u.sevenZipReader.File
	length := len(files)
	maxSize := uint64(u.MaxSize)

	if length == 0 || u.iterationEnded || u.fileIndex >= length {
		u.iterationEnded = true
		return false, nil
	}

	// Load current file
	name, content, size, err := u.unpack7zFile(u.fileIndex)
	if err != nil {
		u.iterationEnded = true
		return false, fmt.Errorf("error unpacking 7z file: %w", err)
	}
	u.CurrentFilename = name
	u.CurrentFileContent = content
	u.CurrentFileSize = size

	// Buffer next
	found := false
	for i := u.fileIndex + 1; i < length; i++ {
		f := files[i]
		fileGoodToUnpack := fileGoodToUnpack(u.Blacklist, u.Whitelist, f.Name)
		isFile := !f.FileInfo().IsDir()
		isBelowMaxSize := f.UncompressedSize <= maxSize
		if fileContentsShouldBeUnpacked(isFile, isBelowMaxSize, fileGoodToUnpack) {
			if u.is7zTextFileWithContent(i) {
				u.fileIndex = i
				name, content, size, err := u.unpack7zFile(i)
				if err != nil {
					u.iterationEnded = true
					return true, fmt.Errorf("error unpacking buffered 7z file: %w", err)
				}
				u.bufferedFilename = name
				u.bufferedFileContent = content
				u.bufferedFileSize = size
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

	switch filepath.Ext(u.Archive) {
	case ".zip":
		return u.findFirstZip()
	case ".tar":
		return u.findFirstTar()
	case ".7z":
		return u.findFirst7z()
	default:
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

	switch filepath.Ext(u.Archive) {
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
