package utils

import (
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestReadZipFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/test.zip",
			expected: []structs.File{
				{Path: "../../testdata/test.zip", Name: "test/", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.zip", Name: "test/file2", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.zip", Name: "test/file1.txt", Size: 6, Suffix: ".txt"},
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
			filepath: "../../testdata/test.tar",
			expected: []structs.File{
				{Path: "../../testdata/test.tar", Name: "test/", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar", Name: "test/file2", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar", Name: "test/file1.txt", Size: 6, Suffix: ".txt"},
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
			filepath: "../../testdata/test.tar.gz",
			expected: []structs.File{
				{Path: "../../testdata/test.tar.gz", Name: "test/", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar.gz", Name: "test/file2", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar.gz", Name: "test/file1.txt", Size: 6, Suffix: ".txt"},
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
			file: structs.File{Path: "../../testdata/test.zip", Suffix: ".zip"},
			expected: []structs.File{
				{Path: "../../testdata/test.zip", Name: "test/", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.zip", Name: "test/file2", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.zip", Name: "test/file1.txt", Size: 6, Suffix: ".txt"},
			},
		},
		{
			file: structs.File{Path: "../../testdata/test.tar", Suffix: ".tar"},
			expected: []structs.File{
				{Path: "../../testdata/test.tar", Name: "test/", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar", Name: "test/file2", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar", Name: "test/file1.txt", Size: 6, Suffix: ".txt"},
			},
		},
		{
			file: structs.File{Path: "../../testdata/test.tar.gz", Suffix: ".tar.gz"},
			expected: []structs.File{
				{Path: "../../testdata/test.tar.gz", Name: "test/", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar.gz", Name: "test/file2", Size: 0, Suffix: ""},
				{Path: "../../testdata/test.tar.gz", Name: "test/file1.txt", Size: 6, Suffix: ".txt"},
			},
		},
		{
			file:     structs.File{Path: "../../testdata/config.toml.test"},
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
