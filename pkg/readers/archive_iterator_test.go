package readers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIterareUnpackedFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Test with zip file", "../../testdata/test.zip"},
		{"Test with tar file", "../../testdata/test.tar"},
		{"Test with 7z file", "../../testdata/test.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := NewUnpackedFileIterator(test.filepath, 1024*1024)
			assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")

			assert.True(t, nfi.Next())
			assert.True(t, nfi.HasNext())

			assert.True(t, nfi.Next())
			assert.False(t, nfi.HasNext())
		})
	}
}

func TestIterareEmpty(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Empty zip", "../../testdata/more_archives/only_folders.zip"},
		{"Empty tar", "../../testdata/more_archives/only_folders.tar"},
		{"Empty 7z", "../../testdata/more_archives/only_folders.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := NewUnpackedFileIterator(test.filepath, 1024*1024)
			assert.False(t, nfi.HasFilesToUnpack(), "Expected no valid files in archive")
			assert.False(t, nfi.HasNext())
		})
	}
}

func TestIterareUnpackedFilesMaxSize(t *testing.T) {
	tests := []struct {
		name        string
		filepath    string
		maxLen      int
		expectedLen int
	}{
		{"Zip with one file excluded", "../../testdata/test.zip", 5, 1},
		{"Zip with all files accepted", "../../testdata/test.zip", 10, 2},
		{"Tar with one file excluded", "../../testdata/test.tar", 5, 1},
		{"Tar with all files accepted", "../../testdata/test.tar", 10, 2},
		{"7z with one file excluded", "../../testdata/test.7z", 5, 1},
		{"7z with all files accepted", "../../testdata/test.7z", 10, 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := NewUnpackedFileIterator(test.filepath, test.maxLen)

			if !nfi.HasFilesToUnpack() {
				assert.Equal(t, 0, test.expectedLen, "No files to unpack, but expected some")
				return
			}

			count := 0
			for nfi.HasNext() {
				assert.True(t, nfi.Next())
				count++
			}

			assert.Equal(t, test.expectedLen, count)
		})
	}
}

func TestIteratorEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		filepath    string
		maxSize     int
		expectedLen int
	}{
		{"Empty ZIP", "../../testdata/more_archives/empty.zip", 1024, 0},
		{"Empty TAR", "../../testdata/more_archives/empty.tar", 1024, 0},
		{"Empty 7Z", "../../testdata/more_archives/empty.7z", 1024, 0},
		{"Huge ZIP", "../../testdata/more_archives/huge_file.zip", 1024, 0},
		{"Huge TAR", "../../testdata/more_archives/huge_file.tar", 1024, 0},
		{"Huge 7Z", "../../testdata/more_archives/huge_file.7z", 1024, 0},
		{"Mixed ZIP", "../../testdata/more_archives/mixed.zip", 1024, 1},
		{"Mixed TAR", "../../testdata/more_archives/mixed.tar", 1024, 1},
		{"Mixed 7Z", "../../testdata/more_archives/mixed.7z", 1024, 1},
		{"Mixed ZIP All", "../../testdata/more_archives/mixed.zip", 20000, 2},
		{"Mixed TAR All", "../../testdata/more_archives/mixed.tar", 20000, 2},
		{"Mixed 7Z All", "../../testdata/more_archives/mixed.7z", 20000, 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := NewUnpackedFileIterator(test.filepath, test.maxSize)

			if test.expectedLen == 0 {
				assert.False(t, nfi.HasFilesToUnpack(), "Expected no files in archive")
				assert.False(t, nfi.HasNext())
				return
			}

			assert.True(t, nfi.HasFilesToUnpack(), "Expected files in archive")

			count := 0
			for nfi.HasNext() {
				assert.True(t, nfi.Next(), "Expected valid file")
				assert.NotEmpty(t, nfi.CurrentFilename)
				assert.Greater(t, len(nfi.CurrentFileContent), 0)
				count++
			}

			assert.Equal(t, test.expectedLen, count)
			assert.False(t, nfi.HasNext())
		})
	}
}

