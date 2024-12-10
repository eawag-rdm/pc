package checks

import (
	"os"
	"testing"

	"github.com/eawag-rdm/pc/pkg/utils"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func tempFile(content []byte) string {
	file, err := os.CreateTemp("", "go-testing")
	check(err)
	_, err = file.Write(content)
	check(err)
	return file.Name()
}

func TestHasOnlyASCII(t *testing.T) {
	tests := []struct {
		name     string
		file     utils.File
		expected []utils.Message
	}{
		{
			name:     "ASCII only",
			file:     utils.File{Name: "testfile.txt"},
			expected: nil,
		},
		{
			name:     "ASCII only but space",
			file:     utils.File{Name: "test file.txt"},
			expected: nil,
		},
		{
			name: "Non-ASCII character",
			file: utils.File{Name: "testfile_ñ_ñ.txt"},
			expected: []utils.Message{
				{Content: "File contains non-ASCII character: ññ", Source: utils.File{Name: "testfile_ñ_ñ.txt"}},
			},
		},
		{
			name: "Mixed ASCII and non-ASCII characters",
			file: utils.File{Name: "testfile_abc_ñ_123.txt"},
			expected: []utils.Message{

				{Content: "File contains non-ASCII character: ñ", Source: utils.File{Name: "testfile_abc_ñ_123.txt"}},
			},
		},
		{
			name:     "Empty file name",
			file:     utils.File{Name: ""},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasOnlyASCII(tt.file)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
			for i := range result {
				if result[i].Content != tt.expected[i].Content {
					t.Errorf("expected %v, got %v", tt.expected[i].Content, result[i].Content)
				}
			}
		})
	}
}
func TestHasNoWhiteSpace(t *testing.T) {
	tests := []struct {
		name     string
		file     utils.File
		expected []utils.Message
	}{
		{
			name:     "No spaces",
			file:     utils.File{Name: "testfile.txt"},
			expected: nil,
		},
		{
			name: "Contains spaces",
			file: utils.File{Name: "test file.txt"},
			expected: []utils.Message{
				{Content: "File contains spaces.", Source: utils.File{Name: "test file.txt"}},
			},
		},
		{
			name:     "Empty file name",
			file:     utils.File{Name: ""},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasNoWhiteSpace(tt.file)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
			for i := range result {
				if result[i].Content != tt.expected[i].Content {
					t.Errorf("expected %v, got %v", tt.expected[i].Content, result[i].Content)
				}
			}
		})
	}
}

func TestIsBinaryFile(t *testing.T) {
	// Test cases
	var binTests = []struct {
		file     string // input
		expected bool   // expected result
	}{
		{tempFile([]byte{101, 111, 0, 222}), true},
		{tempFile([]byte("Hello")), false},
	}

	// Loop over test cases
	for _, tt := range binTests {
		isBin, _ := isBinaryFile(tt.file) // Call the function being tested
		if isBin != tt.expected {
			t.Errorf("Error for '%v': got %v, want %v", tt.file, isBin, tt.expected)
		}
	}
}
func TestIsFreeOfKeywords(t *testing.T) {
	tests := []struct {
		name     string
		file     utils.File
		keywords []string
		info     string
		content  []byte
		expected []utils.Message
	}{
		{
			name:     "No keywords",
			file:     utils.File{Path: tempFile([]byte("This is a test file without keywords."))},
			keywords: []string{"keyword1", "keyword2"},
			info:     "Keywords found:",
			content:  []byte("This is a test file without keywords."),
			expected: nil,
		},
		{
			name:     "Single keyword",
			file:     utils.File{Path: tempFile([]byte("This file contains keyword1."))},
			keywords: []string{"keyword1", "keyword2"},
			info:     "Keywords found:",
			content:  []byte("This file contains keyword1."),
			expected: []utils.Message{{Content: "Keywords found: keyword1", Source: utils.File{Path: tempFile([]byte("This file contains keyword1."))}}},
		},
		{
			name:     "Multiple keywords",
			file:     utils.File{Path: tempFile([]byte("This file contains keyword1 and keyword2."))},
			keywords: []string{"keyword1", "keyword2"},
			info:     "Keywords found:",
			content:  []byte("This file contains keyword1 and keyword2."),
			expected: []utils.Message{{Content: "Keywords found: keyword1, keyword2", Source: utils.File{Path: tempFile([]byte("This file contains keyword1 and keyword2."))}}},
		},
		{
			name:     "Binary file",
			file:     utils.File{Path: tempFile([]byte{0x00, 0x01, 0x02})},
			keywords: []string{"keyword1", "keyword2"},
			info:     "Keywords found:",
			content:  []byte{0x00, 0x01, 0x02},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsFreeOfKeywords(tt.file, tt.keywords, tt.info)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
			for i := range result {
				if result[i].Content != tt.expected[i].Content {
					t.Errorf("expected %v, got %v", tt.expected[i].Content, result[i].Content)
				}
			}
		})
	}
}

func TestIsValidName(t *testing.T) {
	tests := []struct {
		name             string
		file             utils.File
		invalidFileNames []string
		expected         []utils.Message
	}{
		{
			name:             "Valid file name",
			file:             utils.File{Name: "validfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected:         nil,
		},
		{
			name:             "Invalid file name",
			file:             utils.File{Name: "invalidfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected: []utils.Message{
				{Content: "File has an invalid name. invalidfile.txt", Source: utils.File{Name: "invalidfile.txt"}},
			},
		},
		{
			name:             "Another invalid file name",
			file:             utils.File{Name: "badfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected: []utils.Message{
				{Content: "File has an invalid name. badfile.txt", Source: utils.File{Name: "badfile.txt"}},
			},
		},
		{
			name:             "Empty file name",
			file:             utils.File{Name: ""},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected:         nil,
		},
		{
			name:             "No invalid file names",
			file:             utils.File{Name: "somefile.txt"},
			invalidFileNames: []string{},
			expected:         nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidName(tt.file, tt.invalidFileNames)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
			for i := range result {
				if result[i].Content != tt.expected[i].Content {
					t.Errorf("expected %v, got %v", tt.expected[i].Content, result[i].Content)
				}
			}
		})
	}
}
