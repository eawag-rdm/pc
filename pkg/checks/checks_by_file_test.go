package checks

import (
	"os"
	"strings"
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

func TestIsFileNameTooLong(t *testing.T) {
	var config = config.Config{}
	tests := []struct {
		name     string
		file     structs.File
		expected []structs.Message
	}{
		{
			name:     "Not too long",
			file:     structs.File{Name: "This-is-okay.txt"},
			expected: nil,
		},
		{
			name: "Too long",
			file: structs.File{Name: "ThisFilenameIsTooooooooooooLooooooooooooooooooooooooooooooong.txt"},
			expected: []structs.Message{
				{Content: "File name is too long.", Source: structs.File{Name: "ThisFilenameIsTooooooooooooLooooooooooooooooooooooooooooooong.txt"}},
			},
		},
		{
			name:     "Empty file name",
			file:     structs.File{Name: ""},
			expected: nil,
		},
		{
			name:     "Exactly max ASCII length",
			file:     structs.File{Name: strings.Repeat("a", 64)},
			expected: nil,
		},
		{
			name: "One byte over ASCII limit",
			file: structs.File{Name: strings.Repeat("b", 65)},
			expected: []structs.Message{
				{Content: "File name is too long.", Source: structs.File{Name: strings.Repeat("b", 65)}},
			},
		},
		{
			name:     "Multi-byte runes exactly 64 bytes (32× 'é')",
			file:     structs.File{Name: strings.Repeat("é", 32)}, // 32×2 bytes = 64
			expected: nil,
		},
		{
			name: "Multi-byte runes over 64 bytes (33× 'é')",
			file: structs.File{Name: strings.Repeat("é", 33)}, // 33×2 bytes = 66
			expected: []structs.Message{
				{Content: "File name is too long.", Source: structs.File{Name: strings.Repeat("é", 33)}},
			},
		},
		{
			name:     "Whitespace only at limit",
			file:     structs.File{Name: strings.Repeat(" ", 64)},
			expected: nil,
		},
		{
			name: "Whitespace only over limit",
			file: structs.File{Name: strings.Repeat(" ", 65)},
			expected: []structs.Message{
				{Content: "File name is too long.", Source: structs.File{Name: strings.Repeat(" ", 65)}},
			},
		},
		{
			name: "Emoji pushes over limit",
			file: structs.File{Name: strings.Repeat("👍", 17)}, // 17×4 bytes = 68
			expected: []structs.Message{
				{Content: "File name is too long.", Source: structs.File{Name: strings.Repeat("👍", 17)}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsFileNameTooLong(tt.file, config)
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

func TestHasFileNameSpecialChars(t *testing.T) {
	var config = config.Config{}
	tests := []struct {
		name     string
		file     structs.File
		expected []structs.Message
	}{
		{
			name:     "Not too long",
			file:     structs.File{Name: "This-is-okay.txt"},
			expected: nil,
		},
		{
			name:     "Too long",
			file:     structs.File{Name: "This_is_okay.txt"},
			expected: nil,
		},
		{
			name:     "Empty file name",
			file:     structs.File{Name: "This is also okayissch.abc"},
			expected: nil,
		},
		{
			name: "This is bad",
			file: structs.File{Name: "!Attention.xlsx"},
			expected: []structs.Message{
				{Content: "File name contains invalid character: '!'", Source: structs.File{Name: "!Attention.xlsx"}},
			},
		},
		{
			name:     "This is okay",
			file:     structs.File{Name: "\x32\x31.xlsx"},
			expected: nil,
		},
		{
			name:     "Empty filename",
			file:     structs.File{Name: ""},
			expected: nil,
		},
		// 2) Special char at start (backtick)
		{
			name: "Starts with backtick",
			file: structs.File{Name: "`script.sh"},
			expected: []structs.Message{
				{
					Content: "File name contains invalid character: '`'",
					Source:  structs.File{Name: "`script.sh"},
				},
			},
		},
		// 3) Special char in middle (hash)
		{
			name: "Contains hash",
			file: structs.File{Name: "myfile#v2.doc"},
			expected: []structs.Message{
				{
					Content: "File name contains invalid character: '#'",
					Source:  structs.File{Name: "myfile#v2.doc"},
				},
			},
		},
		// 4) Multiple specials—only first is reported ('[')
		{
			name: "Multiple specials",
			file: structs.File{Name: "file[name]{ok}.txt"},
			expected: []structs.Message{
				{
					Content: "File name contains invalid character: '['",
					Source:  structs.File{Name: "file[name]{ok}.txt"},
				},
			},
		},
		// 5) Curly-brace at end
		{
			name: "Ends with brace",
			file: structs.File{Name: "report}.pdf"},
			expected: []structs.Message{
				{
					Content: "File name contains invalid character: '}'",
					Source:  structs.File{Name: "report}.pdf"},
				},
			},
		},
		// 6) Non-ASCII rune (e.g. “é”)—should pass unless you explicitly forbid ≥128
		{
			name:     "Non-ASCII allowed",
			file:     structs.File{Name: "café.txt"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasFileNameSpecialChars(tt.file, config)
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
		{
			name:     "Path",
			file:     structs.File{Path: tempFile([]byte("This is some text."))},
			keywords: "/Users/",
			info:     "Keywords found:",
			content:  []byte("This is some text."),
			expected: nil,
		},
		{
			name:     "Path2",
			file:     structs.File{Path: tempFile([]byte("ADHABDAID /Users/"))},
			keywords: "/Users/",
			info:     "Keywords found:",
			content:  []byte("ADHABDAID /Users/"),
			expected: []structs.Message{{Content: "Keywords found: '/Users/'", Source: structs.File{Path: tempFile([]byte("ADHABDAID /Users/"))}}},
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
				{Content: "File or Folder has an invalid name: invalidfile.txt", Source: structs.File{Name: "invalidfile.txt"}},
			},
		},
		{
			name:             "Another invalid file name",
			file:             structs.File{Name: "badfile.txt"},
			invalidFileNames: []string{"invalidfile.txt", "badfile.txt"},
			expected: []structs.Message{
				{Content: "File or Folder has an invalid name: badfile.txt", Source: structs.File{Name: "badfile.txt"}},
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
		{
			name:             "Is invalid case instead of invalid name",
			file:             structs.File{Name: "invalidfile.txt"},
			invalidFileNames: []string{"Invalidfile.txt", "badfile.txt"},
			expected:         []structs.Message{{Content: "File or Folder has an invalid name: invalidfile.txt", Source: structs.File{Name: "invalidfile.txt"}}},
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

func TestIsValidNameExtended(t *testing.T) {
	tests := []struct {
		name                 string
		file                 structs.File
		disallowedNames      []string
		expectedMessageCount int
	}{
		{
			name:                 "Checking file endings.",
			file:                 structs.File{Name: "abc.doc"},
			disallowedNames:      []string{".doc", ".xls"},
			expectedMessageCount: 1,
		},
		{
			name:                 "Folder in the file name",
			file:                 structs.File{Name: "__pycache__/invalidfile.txt"},
			disallowedNames:      []string{"__pycache__", "invalidfile.txt", ".txt"},
			expectedMessageCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidNameCore(tt.file, tt.disallowedNames)
			if len(result) != tt.expectedMessageCount {
				t.Errorf("expected %v messages, got %v", tt.expectedMessageCount, result)
			}
		})
	}
}

func TestIsTextFile(t *testing.T) {
	tests := []struct {
		name     string
		content  []byte
		expected bool
	}{
		{
			name:     "Text file",
			content:  []byte("This is a plain text file."),
			expected: true,
		},
		{
			name:     "Binary file",
			content:  []byte{0x00, 0x01, 0x02, 0x03, 0x04},
			expected: false,
		},
		{
			name:     "Empty file",
			content:  []byte{},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := tempFile(tt.content)
			defer os.Remove(filePath)

			result, err := isTextFile(filePath)
			if err != nil {
				t.Errorf("Error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsTextFileExampleFiles(t *testing.T) {
	tests := []struct {
		filepath string
		expected bool
	}{
		{
			filepath: "../../testdata/readme.txt",
			expected: true,
		},
		{
			filepath: "../../testdata/test_ckan_metadata.json",
			expected: true,
		},
		{
			filepath: "../../testdata/test_config.toml",
			expected: true,
		},
		{
			filepath: "../../testdata/archives/test.7z",
			expected: false,
		},
		{
			filepath: "../../testdata/test.docx",
			expected: false,
		},
		{
			filepath: "../../testdata/test.xml",
			expected: true,
		},
		{
			filepath: "../../testdata/test.html",
			expected: true,
		},
	}
	for _, test := range tests {
		actual, err := isTextFile(test.filepath)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if actual != test.expected {
			t.Errorf("File: %s Expected: %v, Actual: %v", test.filepath, test.expected, actual)
		}
	}
}
func TestIsArchiveFreeOfKeywordsWithRealArchives(t *testing.T) {
	configPath := "../../testdata/test_config.toml"
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg.Tests["IsFreeOfKeywords"].Whitelist = []string{}
	cfg.Tests["IsFreeOfKeywords"].Blacklist = []string{}

	zipFile := structs.File{Path: "../../testdata/archives/complex_archive.zip", Name: "complex_archive.zip", IsArchive: true}
	sevenZipFile := structs.File{Path: "../../testdata/archives/complex_archive.7z", Name: "complex_archive.7z", IsArchive: true}
	tarFile := structs.File{Path: "../../testdata/archives/complex_archive.tar", Name: "complex_archive.tar", IsArchive: true}
	tests := []struct {
		name     string
		file     structs.File
		expected []structs.Message
	}{
		{
			name: "Complex zip archive",
			file: zipFile,
			expected: []structs.Message{
				{Content: "Possible credentials in file 'User'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: zipFile},
				{Content: "Possible internal information in file 'Q:'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: zipFile},
				{Content: "Do you have hardcoded filepaths in your files?  Found suspicious keyword(s): '/Users/'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: zipFile},
				{Content: "Possible credentials in file 'PASSWORD', 'USER'. In archived file: 'complex_archive/hasKeywords'", Source: zipFile},
				{Content: "Possible credentials in file 'Password'. In archived file: 'complex_archive/nested/hasKeywords.md'", Source: zipFile},
				{Content: "Possible internal information in file 'Q:'. In archived file: 'complex_archive/nested/hasKeywords.md'", Source: zipFile},
			},
		},
		{
			name: "Complex 7z archive",
			file: sevenZipFile,
			expected: []structs.Message{
				{Content: "Possible credentials in file 'User'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: sevenZipFile},
				{Content: "Possible internal information in file 'Q:'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: sevenZipFile},
				{Content: "Do you have hardcoded filepaths in your files?  Found suspicious keyword(s): '/Users/'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: sevenZipFile},
				{Content: "Possible credentials in file 'PASSWORD', 'USER'. In archived file: 'complex_archive/hasKeywords'", Source: sevenZipFile},
				{Content: "Possible credentials in file 'Password'. In archived file: 'complex_archive/nested/hasKeywords.md'", Source: sevenZipFile},
				{Content: "Possible internal information in file 'Q:'. In archived file: 'complex_archive/nested/hasKeywords.md'", Source: sevenZipFile},
			},
		},
		{
			name: "Complex tar archive",
			file: tarFile,
			expected: []structs.Message{
				{Content: "Possible credentials in file 'User'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: tarFile},
				{Content: "Possible internal information in file 'Q:'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: tarFile},
				{Content: "Do you have hardcoded filepaths in your files?  Found suspicious keyword(s): '/Users/'. In archived file: 'complex_archive/alsoHasKeywords.py'", Source: tarFile},
				{Content: "Possible credentials in file 'PASSWORD', 'USER'. In archived file: 'complex_archive/hasKeywords'", Source: tarFile},
				{Content: "Possible credentials in file 'Password'. In archived file: 'complex_archive/nested/hasKeywords.md'", Source: tarFile},
				{Content: "Possible internal information in file 'Q:'. In archived file: 'complex_archive/nested/hasKeywords.md'", Source: tarFile},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsArchiveFreeOfKeywords(tt.file, *cfg)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
			for i := range result {
				inExpected := false
				for j := range tt.expected {
					if result[i].Content == tt.expected[j].Content {
						inExpected = true
						break
					}
				}
				if !inExpected {
					t.Errorf("unexpected message: %v", result[i].Content)
				}
				if result[i].Source != tt.expected[i].Source {
					t.Errorf("expected %v, got %v", tt.expected[i].Source, result[i].Source)
				}
			}
		})
	}
}
