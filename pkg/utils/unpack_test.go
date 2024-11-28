package utils

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"testing"
)

func TestReadTarGzFileList(t *testing.T) {
	// Create a temporary tar.gz file for testing
	tempFile, err := os.CreateTemp("", "test*.tar.gz")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	// Create a new tar.gz writer
	gzipWriter := gzip.NewWriter(tempFile)
	tarWriter := tar.NewWriter(gzipWriter)

	// Add a file to the tar.gz archive
	header := &tar.Header{
		Name: "testfile.txt",
		Mode: 0600,
		Size: int64(len("This is a test file")),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("Failed to write header to tar: %v", err)
	}
	if _, err := tarWriter.Write([]byte("This is a test file")); err != nil {
		t.Fatalf("Failed to write file to tar: %v", err)
	}
	tarWriter.Close()
	gzipWriter.Close()

	// Test ReadTarGzFileList function
	fileList, err := ReadTarGzFileList(tempFile.Name())
	if err != nil {
		t.Fatalf("ReadTarGzFileList returned an error: %v", err)
	}

	// Check if the file list contains the expected file
	expectedFile := "testfile.txt"
	found := false
	for _, file := range fileList {
		if file == expectedFile {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected file %s not found in file list", expectedFile)
	}
}

func TestReadTarFileList(t *testing.T) {
	// Create a temporary tar file for testing
	tempFile, err := os.CreateTemp("", "test*.tar")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	// Create a new tar writer
	tarWriter := tar.NewWriter(tempFile)

	// Add a file to the tar archive
	header := &tar.Header{
		Name: "testfile.txt",
		Mode: 0600,
		Size: int64(len("This is a test file")),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("Failed to write header to tar: %v", err)
	}
	if _, err := tarWriter.Write([]byte("This is a test file")); err != nil {
		t.Fatalf("Failed to write file to tar: %v", err)
	}
	tarWriter.Close()

	// Test ReadTarFileList function
	fileList, err := ReadTarFileList(tempFile.Name())
	if err != nil {
		t.Fatalf("ReadTarFileList returned an error: %v", err)
	}

	// Check if the file list contains the expected file
	expectedFile := "testfile.txt"
	found := false
	for _, file := range fileList {
		if file == expectedFile {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected file %s not found in file list", expectedFile)
	}
}

func TestReadZipFileList(t *testing.T) {
	// Create a temporary zip file for testing
	tempFile, err := os.CreateTemp("", "test*.zip")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())

	// Create a new zip writer
	zipWriter := zip.NewWriter(tempFile)

	// Add a file to the zip archive
	header := &zip.FileHeader{
		Name:   "testfile.txt",
		Method: zip.Store,
	}
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		t.Fatalf("Failed to create header in zip: %v", err)
	}
	if _, err := writer.Write([]byte("This is a test file")); err != nil {
		t.Fatalf("Failed to write file to zip: %v", err)
	}
	zipWriter.Close()

	// Test ReadZipFileList function
	fileList, err := ReadZipFileList(tempFile.Name())
	if err != nil {
		t.Fatalf("ReadZipFileList returned an error: %v", err)
	}

	// Check if the file list contains the expected file
	expectedFile := "testfile.txt"
	found := false
	for _, file := range fileList {
		if file == expectedFile {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected file %s not found in file list", expectedFile)
	}
}
