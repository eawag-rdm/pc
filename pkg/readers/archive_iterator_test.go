package readers

import (
	"fmt"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestIterareUnpackedFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Test with zip file", "../../testdata/archives/test.zip"},
		{"Test with tar file", "../../testdata/archives/test.tar"},
		{"Test with 7z file", "../../testdata/archives/test.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, 1024*1024, []string{}, []string{})
			assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
			count := 0
			for nfi.HasNext() {
				nfi.Next()
				name, _, _ := nfi.UnpackedFile()
				assert.NotEmpty(t, name, "File name should not be empty")
				count++
			}
			assert.Equal(t, 1, count, "1 File expected in archive, as the second one is empty.")
		})
	}
}

func TestValidFileCount(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Test with zip file", "../../testdata/archives/ten_valid_files.zip"},
		{"Test with tar file", "../../testdata/archives/ten_valid_files.tar"},
		{"Test with 7z file", "../../testdata/archives/ten_valid_files.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, 1024*1024, []string{}, []string{})
			assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
			count := 0
			for nfi.HasNext() {
				nfi.Next()
				name, _, _ := nfi.UnpackedFile()
				assert.NotEmpty(t, name, "File name should not be empty")
				count++
			}
			assert.Equal(t, 10, count, "10 files expected in archive.")
		})
	}
}

func TestIterareEmpty(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Empty zip", "../../testdata/archives/only_folders.zip"},
		{"Empty tar", "../../testdata/archives/only_folders.tar"},
		{"Empty 7z", "../../testdata/archives/only_folders.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, 1024*1024, []string{}, []string{})
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
		{"Zip with 2 files excluded (one empty, one too large)", "../../testdata/archives/test.zip", 5, 0},
		{"Zip with one file accepted (one empty)", "../../testdata/archives/test.zip", 10, 1},
		{"Tar with 2 files excluded (one empty, one too large)", "../../testdata/archives/test.tar", 5, 0},
		{"Tar with one file accepted (one empty)", "../../testdata/archives/test.tar", 10, 1},
		{"7z with 2 files excluded (one empty, one too large)", "../../testdata/archives/test.7z", 5, 0},
		{"7z wwith one file accepted (one empty)", "../../testdata/archives/test.7z", 10, 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, test.maxLen, []string{}, []string{})

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
		{"Empty ZIP", "../../testdata/archives/empty.zip", 1024, 0},
		{"Empty TAR", "../../testdata/archives/empty.tar", 1024, 0},
		{"Empty 7Z", "../../testdata/archives/empty.7z", 1024, 0},
		{"Huge ZIP", "../../testdata/archives/huge_file.zip", 1024, 0},
		{"Huge TAR", "../../testdata/archives/huge_file.tar", 1024, 0},
		{"Huge 7Z", "../../testdata/archives/huge_file.7z", 1024, 0},
		{"Mixed ZIP", "../../testdata/archives/mixed.zip", 1024, 1},
		{"Mixed TAR", "../../testdata/archives/mixed.tar", 1024, 1},
		{"Mixed 7Z", "../../testdata/archives/mixed.7z", 1024, 1},
		{"Mixed ZIP All", "../../testdata/archives/mixed.zip", 20000, 2},
		{"Mixed TAR All", "../../testdata/archives/mixed.tar", 20000, 2},
		{"Mixed 7Z All", "../../testdata/archives/mixed.7z", 20000, 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, test.maxSize, []string{}, []string{})

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
func TestInitArchiveIterator(t *testing.T) {
	tests := []struct {
		name         string
		archive      structs.File
		maxSize      int
		expectedPath string
	}{
		{
			name: "Archive path with name included",
			archive: structs.File{
				Path: "../../testdata/archives/test.zip",
				Name: "test.zip",
			},
			maxSize:      1024,
			expectedPath: "../../testdata/archives/test.zip",
		},
		{
			name: "Archive path without name included",
			archive: structs.File{
				Path: "../../testdata/archives",
				Name: "test.zip",
			},
			maxSize:      1024,
			expectedPath: "../../testdata/archives/test.zip",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			iterator := InitArchiveIterator(test.archive, test.maxSize, []string{}, []string{})

			assert.Equal(t, test.expectedPath, iterator.Archive, "Archive path mismatch")
			assert.Equal(t, test.maxSize, iterator.MaxSize, "MaxSize mismatch")
			assert.Empty(t, iterator.CurrentFilename, "CurrentFilename should be empty")
			assert.Empty(t, iterator.CurrentFileContent, "CurrentFileContent should be empty")
			assert.Zero(t, iterator.CurrentFileSize, "CurrentFileSize should be zero")
			assert.Empty(t, iterator.bufferedFilename, "bufferedFilename should be empty")
			assert.Empty(t, iterator.bufferedFileContent, "bufferedFileContent should be empty")
			assert.Zero(t, iterator.bufferedFileSize, "bufferedFileSize should be zero")
			assert.False(t, iterator.iterationEnded, "iterationEnded should be false")
			assert.False(t, iterator.hasCheckedFirstFile, "hasCheckedFirstFile should be false")
			assert.Equal(t, -1, iterator.fileIndex, "fileIndex should be -1")
			assert.Nil(t, iterator.tarFile, "tarFile should be nil")
			assert.Nil(t, iterator.tarReader, "tarReader should be nil")
			assert.Nil(t, iterator.zipReader, "zipReader should be nil")
			assert.Nil(t, iterator.sevenZipReader, "sevenZipReader should be nil")
		})
	}
}

func TestFiltersDuringArchiveIteration(t *testing.T) {
	// The one_of_each archives contain:
	// - an empty file
	// - a valid file with a size of 175 kB
	// - a valid file with a size of 1.2 MB
	// - a valid file with a size of 2.3 MB
	// - a binary file with a size of 1 MB
	// - a valid file to whitelist
	// - a valid file to blacklist
	baseTests := []struct {
		name          string
		baseFile      string
		maxLen        int
		whitelist     []string
		blacklist     []string
		unpackedFiles []string
	}{
		{"Archive with maxSize filter", "one_of_each", 2 * 1024 * 1024, []string{}, []string{}, []string{"large_valid.txt", "very_large_but_valid.txt", "black/to_be_blacklisted.blst", "white/to_be_whitelisted.wlst"}},
		{"Archive with smaller maxSize filter", "one_of_each", 0.5 * 1024 * 1024, []string{}, []string{}, []string{"large_valid.txt", "black/to_be_blacklisted.blst", "white/to_be_whitelisted.wlst"}},
		{"Archive with whitelist filter", "one_of_each", 2 * 1024 * 1024, []string{".wlst"}, []string{}, []string{"white/to_be_whitelisted.wlst"}},
		{"Archive with whitelist filter 2", "one_of_each", 2 * 1024 * 1024, []string{"to_be_whitelisted"}, []string{}, []string{"white/to_be_whitelisted.wlst"}},
		{"Archive with blacklist filter", "one_of_each", 2 * 1024 * 1024, []string{}, []string{".blst"}, []string{"large_valid.txt", "very_large_but_valid.txt", "white/to_be_whitelisted.wlst"}},
		{"Archive with overlapping filters", "one_of_each", 0.5 * 1024 * 1024, []string{}, []string{".blst"}, []string{"large_valid.txt", "white/to_be_whitelisted.wlst"}},
		{"Archive with overlapping filters 2", "one_of_each", 10, []string{"wlst"}, []string{}, []string{}},
	}

	var tests []struct {
		name          string
		filepath      string
		maxLen        int
		whitelist     []string
		blacklist     []string
		unpackedFiles []string
	}

	formats := []string{".zip", ".7z", ".tar"}

	for _, base := range baseTests {
		for _, ext := range formats {
			tests = append(tests, struct {
				name          string
				filepath      string
				maxLen        int
				whitelist     []string
				blacklist     []string
				unpackedFiles []string
			}{
				name:          fmt.Sprintf("%s (%s)", base.name, ext),
				filepath:      fmt.Sprintf("../../testdata/archives/%s%s", base.baseFile, ext),
				maxLen:        base.maxLen,
				whitelist:     base.whitelist,
				blacklist:     base.blacklist,
				unpackedFiles: base.unpackedFiles,
			})
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, test.maxLen, test.whitelist, test.blacklist)
			if len(test.unpackedFiles) == 0 {
				assert.False(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
			} else {
				assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
				count := 0
				for nfi.HasNext() {
					assert.True(t, nfi.Next())
					name, _, _ := nfi.UnpackedFile()

					// Check if the file name is in the unpacked_files list
					found := false
					for _, unpackedFile := range test.unpackedFiles {
						if name == unpackedFile {
							found = true
							break
						}
					}
					assert.True(t, found, fmt.Sprintf("File '%s' should be in the unpacked_files list", name))
					count++
				}

				assert.Equal(t, len(test.unpackedFiles), count, "Number of unpacked files should match the expected count")
			}
		})
	}
}

func TestSkippingALotOfFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"A lot of empty files in ZIP", "../../testdata/archives/a_lot_of_empty_files.zip"},
		{"A lot of empty files in TAR", "../../testdata/archives/a_lot_of_empty_files.tar"},
		{"A lot of empty files in 7Z", "../../testdata/archives/a_lot_of_empty_files.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, 1024, []string{}, []string{})
			assert.False(t, nfi.HasFilesToUnpack(), "Expected no files in archive")
			assert.False(t, nfi.HasNext())
		})
	}
}

func TestALotOfBinaryFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"A lot of empty files in ZIP", "../../testdata/archives/a_lot_of_binary_files.zip"},
		{"A lot of empty files in 7z", "../../testdata/archives/a_lot_of_binary_files.7z"},
		{"A lot of empty files in TAR", "../../testdata/archives/a_lot_of_binary_files.tar"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nfi := newUnpackedFileIterator(test.filepath, 1024, []string{}, []string{})
			assert.False(t, nfi.HasFilesToUnpack(), "Expected only binary files in archive, so nothing to read")
			assert.False(t, nfi.HasNext())
		})
	}
}
