package checks

import (
	"os"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
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
	var config = config.Config{}
	tests := []struct {
		name     string
		file     structs.File
		expected []structs.Message
	}{
		{
			name:     "ASCII only",
			file:     structs.File{Name: "testfile.txt"},
			expected: nil,
		},
		{
			name:     "ASCII only but space",
			file:     structs.File{Name: "test file.txt"},
			expected: nil,
		},
		{
			name: "Non-ASCII character",
			file: structs.File{Name: "testfile_ñ_ñ.txt"},
			expected: []structs.Message{
				{Content: "File name contains non-ASCII character: ññ", Source: structs.File{Name: "testfile_ñ_ñ.txt"}},
			},
		},
		{
			name: "Mixed ASCII and non-ASCII characters",
			file: structs.File{Name: "testfile_abc_ñ_123.txt"},
			expected: []structs.Message{

				{Content: "File name contains non-ASCII character: ñ", Source: structs.File{Name: "testfile_abc_ñ_123.txt"}},
			},
		},
		{
			name:     "Empty file name",
			file:     structs.File{Name: ""},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasOnlyASCII(tt.file, config)
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
	var config = config.Config{}
	tests := []struct {
		name     string
		file     structs.File
		expected []structs.Message
	}{
		{
			name:     "No spaces",
			file:     structs.File{Name: "testfile.txt"},
			expected: nil,
		},
		{
			name: "Contains spaces",
			file: structs.File{Name: "test file.txt"},
			expected: []structs.Message{
				{Content: "File name contains spaces.", Source: structs.File{Name: "test file.txt"}},
			},
		},
		{
			name:     "Empty file name",
			file:     structs.File{Name: ""},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasNoWhiteSpace(tt.file, config)
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

func TestIsBinaryFileOrNonAscii(t *testing.T) {
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
		isBin, _ := isBinaryFileOrContainsNonAscii(tt.file) // Call the function being tested
		if isBin != tt.expected {
			t.Errorf("Error for '%v': got %v, want %v", tt.file, isBin, tt.expected)
		}
	}
}

func TestIsBinaryFileOrNonAsciiReadme(t *testing.T) {
	tests := []struct {
		filepath string
		expected bool
	}{
		{
			filepath: "../../testdata/readme.txt",
			expected: true,
		},
	}
	for _, test := range tests {
		actual, err := isBinaryFileOrContainsNonAscii(test.filepath)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if actual != test.expected {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}

	}
}

func TestIsFreeOfKeywords(t *testing.T) {
	tests := []struct {
		name     string
		file     structs.File
		keywords string
		info     string
		content  []byte
		expected []structs.Message
	}{
		{
			name:     "No keywords",
			file:     structs.File{Path: tempFile([]byte("This is a test file without keywords."))},
			keywords: "keyword1|keyword2",
			info:     "Keywords found:",
			content:  []byte("This is a test file without keywords."),
			expected: nil,
		},
		{
			name:     "Single keyword",
			file:     structs.File{Path: tempFile([]byte("This file contains keyword1."))},
			keywords: "keyword1|keyword2",
			info:     "Keywords found:",
			content:  []byte("This file contains keyword1."),
			expected: []structs.Message{{Content: "Keywords found: 'keyword1'", Source: structs.File{Path: tempFile([]byte("This file contains keyword1."))}}},
		},
		{
			name:     "Multiple keywords",
			file:     structs.File{Path: tempFile([]byte("This file contains keyword1 and keyword2."))},
			keywords: "keyword1|keyword2",
			info:     "Keywords found:",
			content:  []byte("This file contains keyword1 and keyword2."),
			expected: []structs.Message{{Content: "Keywords found: 'keyword1', 'keyword2'", Source: structs.File{Path: tempFile([]byte("This file contains keyword1 and keyword2."))}}},
		},
		{
			name:     "Binary file",
			file:     structs.File{Path: tempFile([]byte{0x00, 0x01, 0x02})},
			keywords: "keyword1|keyword2",
			info:     "Keywords found:",
			content:  []byte{0x00, 0x01, 0x02},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsFreeOfKeywordsCore(tt.file, tt.keywords, tt.info, [][]byte{tt.content}, false)
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
		file             structs.File
		invalidFileNames []string
		expected         []structs.Message
	}{
		{
			name:             "Valid file name",
			file:             structs.File{Name: "validfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected:         nil,
		},
		{
			name:             "Invalid file name",
			file:             structs.File{Name: "invalidfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected: []structs.Message{
				{Content: "File or Folder has an invalid name. invalidfile.txt", Source: structs.File{Name: "invalidfile.txt"}},
			},
		},
		{
			name:             "Another invalid file name",
			file:             structs.File{Name: "badfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected: []structs.Message{
				{Content: "File or Folder has an invalid name. badfile.txt", Source: structs.File{Name: "badfile.txt"}},
			},
		},
		{
			name:             "Empty file name",
			file:             structs.File{Name: ""},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected:         nil,
		},
		{
			name:             "No invalid file names",
			file:             structs.File{Name: "somefile.txt"},
			invalidFileNames: []string{},
			expected:         nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidNameCore(tt.file, tt.invalidFileNames)
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
