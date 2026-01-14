package readers

import (
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestReadZipFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.zip",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.zip", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.zip"},
			},
		},
	}
	for _, test := range tests {
		actual, err := ReadZipFileList(test.filepath)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}

func TestReadTarFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.tar",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar"},
			},
		},
	}
	for _, test := range tests {
		actual, err := ReadTarFileList(test.filepath)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}

func TestReadTarGzFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.tar.gz",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar.gz"},
			},
		},
	}
	for _, test := range tests {
		actual, err := ReadTarGzFileList(test.filepath)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}
func TestReadArchiveFileList(t *testing.T) {
	tests := []struct {
		file     structs.File
		expected []structs.File
	}{
		{
			file: structs.File{Path: "../../testdata/archives/test.zip", Name: "test.zip", DisplayName: "test.zip", Suffix: ".zip"},
			expected: []structs.File{
				{Path: "../../testdata/archives/test.zip", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.zip"},
			},
		},
		{
			file: structs.File{Path: "../../testdata/archives/test.tar", Name: "test.tar", DisplayName: "test.tar", Suffix: ".tar"},
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar"},
			},
		},
		{
			file: structs.File{Path: "../../testdata/archives/test.tar.gz", Name: "test.tar.gz", DisplayName: "test.tar.gz", Suffix: ".gz"},
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar.gz"},
			},
		},
		{
			file:     structs.File{Path: "../../testdata/config.toml.test", Name: "config.toml.test", DisplayName: "config.toml.test", Suffix: ".test"},
			expected: []structs.File{},
		},
	}
	for _, test := range tests {
		actual, err := ReadArchiveFileList(test.file)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}
func TestRead7ZipFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.7z",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.7z", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.7z"},
				{Path: "../../testdata/archives/test.7z", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.7z"},
				{Path: "../../testdata/archives/test.7z", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.7z"},
			},
		},
	}
	for _, test := range tests {
		actual, err := Read7ZipFileList(test.filepath)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if !reflect.DeepEqual(actual, test.expected) {
			assert.ElementsMatch(t, actual, test.expected)
		}
	}
}
