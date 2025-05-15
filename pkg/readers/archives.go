package readers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/bodgit/sevenzip"
)

// Read the filelist from a zip file
func ReadZipFileList(filePath string) ([]structs.File, error) {
	// Open the zip file for reading
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		
		return nil, err
	}
	defer reader.Close()

	// Read the file list from the zip file
	var fileList []structs.File
	for _, file := range reader.File {
		fileList = append(fileList, structs.ToFile(filePath, file.Name, file.FileInfo().Size(), ""))
	}
	return fileList, nil

}

// Read the filelist from a tar file
func ReadTarFileList(filePath string) ([]structs.File, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

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
		fileList = append(fileList, structs.ToFile(filePath, header.Name, header.Size, ""))
	}
	return fileList, nil
}

// Read the filelist from a tar.gz file
func ReadTarGzFileList(filePath string) ([]structs.File, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

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
		fileList = append(fileList, structs.ToFile(filePath, header.Name, header.Size, ""))
	}
	return fileList, nil
}

func Read7ZipFileList(filePath string) ([]structs.File, error) {
	var fileList []structs.File
	r, err := sevenzip.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	for _, f := range r.File {
		fileList = append(fileList, structs.ToFile(filePath, f.Name, f.FileInfo().Size(), ""))
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
	if strings.HasSuffix(file.Name, ".zip") {
		return ReadZipFileList(file.Path)
	} else if strings.HasSuffix(file.Name, ".tar") {
		return ReadTarFileList(file.Path)
	} else if strings.HasSuffix(file.Name, ".7z") {
		return Read7ZipFileList(file.Path)
	} else if strings.HasSuffix(file.Name, ".tar.gz") {
		fmt.Printf("Not checking contents of tar.gz file: '%s'", file.Name)
		return []structs.File{}, nil
		//return ReadTarGzFileList(file.Path)
	} else {
		return []structs.File{}, nil
	}
}
