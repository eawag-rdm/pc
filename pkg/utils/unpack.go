package utils

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"

	"github.com/eawag-rdm/pc/pkg/structs"
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

func ReadArchiveFileList(file structs.File) ([]structs.File, error) {
	if file.Suffix == ".zip" {
		return ReadZipFileList(file.Path)
	} else if file.Suffix == ".tar" {
		return ReadTarFileList(file.Path)
	} else if file.Suffix == ".tar.gz" {
		return ReadTarGzFileList(file.Path)
	} else {
		return []structs.File{}, nil
	}
}
