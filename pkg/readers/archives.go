package readers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path"
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/bodgit/sevenzip"
)

// Read the filelist from a zip file
func ReadZipFileList(filePath string) ([]structs.File, error) {
	return ReadZipFileListWithDisplayName(filePath, "")
}

// ReadZipFileListWithDisplayName reads the file list with archive display name
func ReadZipFileListWithDisplayName(filePath string, archiveDisplayName string) ([]structs.File, error) {
	// Check if the file exists
	// Open the zip file for reading
	reader, err := zip.OpenReader(filePath)
	if err != nil {

		return nil, err
	}
	defer reader.Close()

	// Use archive filename if display name not provided
	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	// Read the file list from the zip file
	var fileList []structs.File
	for _, file := range reader.File {
		f := structs.ToFileWithDisplay(filePath, file.Name, file.Name, file.FileInfo().Size(), "", archiveDisplayName)
		fileList = append(fileList, f)
	}
	return fileList, nil

}

// Read the filelist from a tar file
func ReadTarFileList(filePath string) ([]structs.File, error) {
	return ReadTarFileListWithDisplayName(filePath, "")
}

// ReadTarFileListWithDisplayName reads the file list with archive display name
func ReadTarFileListWithDisplayName(filePath string, archiveDisplayName string) ([]structs.File, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	tarReader := tar.NewReader(file)
	var fileList []structs.File
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		f := structs.ToFileWithDisplay(filePath, header.Name, header.Name, header.Size, "", archiveDisplayName)
		fileList = append(fileList, f)
	}
	return fileList, nil
}

// Read the filelist from a tar.gz file
func ReadTarGzFileList(filePath string) ([]structs.File, error) {
	return ReadTarGzFileListWithDisplayName(filePath, "")
}

// ReadTarGzFileListWithDisplayName reads the file list with archive display name
func ReadTarGzFileListWithDisplayName(filePath string, archiveDisplayName string) ([]structs.File, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	var fileList []structs.File
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		f := structs.ToFileWithDisplay(filePath, header.Name, header.Name, header.Size, "", archiveDisplayName)
		fileList = append(fileList, f)
	}
	return fileList, nil
}

func Read7ZipFileList(filePath string) ([]structs.File, error) {
	return Read7ZipFileListWithDisplayName(filePath, "")
}

// Read7ZipFileListWithDisplayName reads the file list with archive display name
func Read7ZipFileListWithDisplayName(filePath string, archiveDisplayName string) ([]structs.File, error) {
	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	var fileList []structs.File
	r, err := sevenzip.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	for _, f := range r.File {
		file := structs.ToFileWithDisplay(filePath, f.Name, f.Name, f.FileInfo().Size(), "", archiveDisplayName)
		fileList = append(fileList, file)
	}

	return fileList, nil
}

func IsSupportedArchive(filePath string) bool {
	if strings.HasSuffix(filePath, ".zip") {
		return true
	} else if strings.HasSuffix(filePath, ".tar") {
		return true
	} else if strings.HasSuffix(filePath, ".7z") {
		return true
	} else if strings.HasSuffix(filePath, ".tar.gz") {
		return true
	}
	return false
}

func ReadArchiveFileList(file structs.File) ([]structs.File, error) {
	// Use DisplayName for archive reference (CKAN resource name or filename)
	archiveDisplayName := file.GetDisplayName()

	if strings.HasSuffix(file.Name, ".zip") {
		return ReadZipFileListWithDisplayName(file.Path, archiveDisplayName)
	} else if strings.HasSuffix(file.Name, ".tar") {
		return ReadTarFileListWithDisplayName(file.Path, archiveDisplayName)
	} else if strings.HasSuffix(file.Name, ".7z") {
		return Read7ZipFileListWithDisplayName(file.Path, archiveDisplayName)
	} else if strings.HasSuffix(file.Name, ".tar.gz") {
		return ReadTarGzFileListWithDisplayName(file.Path, archiveDisplayName)
	} else {
		return []structs.File{}, nil
	}
}
