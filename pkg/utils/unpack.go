package utils

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
)

// Read the filelist from a zip file
func ReadZipFileList(zipFile string) ([]string, error) {
	// Open the zip file for reading
	reader, err := zip.OpenReader(zipFile)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	// Read the file list from the zip file
	var fileList []string
	for _, file := range reader.File {
		fileList = append(fileList, file.Name)
	}
	return fileList, nil
}

// Read the filelist from a tar file
func ReadTarFileList(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	tarReader := tar.NewReader(file)
	var fileList []string
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		fileList = append(fileList, header.Name)
	}
	return fileList, nil
}

// Read the filelist from a tar.gz file
func ReadTarGzFileList(filePath string) ([]string, error) {
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
	var fileList []string
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		fileList = append(fileList, header.Name)
	}
	return fileList, nil
}
