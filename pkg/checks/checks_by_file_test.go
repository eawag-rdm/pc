package checks

import (
	"archive/zip"
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/structs"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}

// isFreeOfKeywordsCoreList scans one body with one keyword list, the shape the
// keyword tests assert against - sourced at the file the way an acquisition
// sources it, since the scan itself leaves Source unset. It lives here, not
// beside the production code, because nothing but these tests calls it.
func isFreeOfKeywordsCoreList(file structs.File, keywordList []string, info string, body [][]byte, isBinary bool) []structs.Message {
	report := reportJoined
	if isBinary {
		report = reportIndexed
	}
	set := keywordSet{matcher: optimization.GetMatcher(keywordList), info: info}
	var src structs.Source
	return sourceAll(&src, file, scanKeywords(context.Background(), set, body, lowerAll(body), report))
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
			result := hasOnlyASCII(tt.file)
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
			result := hasNoWhiteSpace(tt.file)
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
			result := isFileNameTooLong(tt.file)
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
		// 4) Multiple specials-only first is reported ('[')
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
		// 6) Non-ASCII rune (e.g. “é”)-should pass unless you explicitly forbid ≥128
		{
			name:     "Non-ASCII allowed",
			file:     structs.File{Name: "café.txt"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasFileNameSpecialChars(tt.file)
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
			result := isFreeOfKeywordsCoreList(tt.file, strings.Split(tt.keywords, "|"), tt.info, [][]byte{tt.content}, false)
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
			result := isValidNameCore(tt.file, tt.invalidFileNames)
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
			result := isValidNameCore(tt.file, tt.disallowedNames)
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

// TestIsArchiveFreeOfKeywordsMemberFilterWiring is the wiring guard for the
// call site: the keyword rules' exclude patterns must reach the iterator in
// the right role. one_of_each holds the keyword "password" in BOTH the .blst
// and the .wlst member, so an exclude naming .blst must silence exactly one of
// them. Swapping include and exclude at the call site inverts both assertions.
func TestIsArchiveFreeOfKeywordsMemberFilterWiring(t *testing.T) {
	const (
		blacklisted = "black/to_be_blacklisted.blst"
		whitelisted = "white/to_be_whitelisted.wlst"
	)

	cfg, err := config.LoadConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	for i := range cfg.Rules {
		if cfg.Rules[i].Check == "IsFreeOfKeywords" {
			cfg.Rules[i].Include = nil
			cfg.Rules[i].Exclude = []string{`\.blst$`}
		}
	}

	archive := structs.File{Path: "../../testdata/archives/one_of_each.zip", Name: "one_of_each.zip", DisplayName: "one_of_each.zip", IsArchive: true}
	hits := map[string]int{}
	for _, m := range runRule(t, "IsFreeOfKeywords", *cfg, ScopeArchiveMember, archive) {
		if f, ok := m.Source.(structs.File); ok && !m.Skipped {
			hits[f.Name]++
		}
	}

	if hits[blacklisted] != 0 {
		t.Errorf("blacklisted member %q produced %d message(s), want none", blacklisted, hits[blacklisted])
	}
	if hits[whitelisted] == 0 {
		t.Errorf("member %q produced no message; the blacklist must not filter it (hits: %v)", whitelisted, hits)
	}
}

func TestIsArchiveFreeOfKeywordsWithRealArchives(t *testing.T) {
	configPath := "../../testdata/test_config.toml"
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	for i := range cfg.Rules {
		if cfg.Rules[i].Check == "IsFreeOfKeywords" {
			cfg.Rules[i].Include = nil
			cfg.Rules[i].Exclude = nil
		}
	}

	zipFile := structs.File{Path: "../../testdata/archives/complex_archive.zip", Name: "complex_archive.zip", DisplayName: "complex_archive.zip", IsArchive: true}
	sevenZipFile := structs.File{Path: "../../testdata/archives/complex_archive.7z", Name: "complex_archive.7z", DisplayName: "complex_archive.7z", IsArchive: true}
	tarFile := structs.File{Path: "../../testdata/archives/complex_archive.tar", Name: "complex_archive.tar", DisplayName: "complex_archive.tar", IsArchive: true}

	// Expected message contents (without archive suffix - now stored in ArchiveName field)
	expectedContents := []string{
		"Possible credentials in file 'User'",
		"Possible internal information in file 'Q:'",
		"Do you have hardcoded filepaths in your files?  Found suspicious keyword(s): '/Users/'",
		"Possible credentials in file 'PASSWORD', 'USER'",
		"Possible credentials in file 'Password'",
		"Possible internal information in file 'Q:'",
	}

	tests := []struct {
		name                string
		file                structs.File
		expectedCount       int
		archiveNameInSource string
	}{
		{
			name:                "Complex zip archive",
			file:                zipFile,
			expectedCount:       6,
			archiveNameInSource: "complex_archive.zip",
		},
		{
			name:                "Complex 7z archive",
			file:                sevenZipFile,
			expectedCount:       6,
			archiveNameInSource: "complex_archive.7z",
		},
		{
			name:                "Complex tar archive",
			file:                tarFile,
			expectedCount:       6,
			archiveNameInSource: "complex_archive.tar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runRule(t, "IsFreeOfKeywords", *cfg, ScopeArchiveMember, tt.file)
			if len(result) != tt.expectedCount {
				t.Errorf("expected %d messages, got %d", tt.expectedCount, len(result))
				for _, r := range result {
					t.Logf("  message: %s", r.Content)
				}
			}

			// Verify each message content is expected
			for _, msg := range result {
				found := false
				for _, expected := range expectedContents {
					if msg.Content == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("unexpected message content: %v", msg.Content)
				}

				// Verify source has ArchiveName set
				if source, ok := msg.Source.(structs.File); ok {
					if source.ArchiveName != tt.archiveNameInSource {
						t.Errorf("expected ArchiveName=%s, got %s", tt.archiveNameInSource, source.ArchiveName)
					}
				} else {
					t.Errorf("expected Source to be structs.File")
				}
			}
		})
	}
}

// newKeywordConfig returns a minimal config with an IsFreeOfKeywords rule
// configured so content scanning is actually attempted (when not size-skipped).
func newKeywordConfig(maxContentScan, maxArchiveFile, maxTotalArchiveMem int64) config.Config {
	return config.Config{
		General: &config.GeneralConfig{
			MaxContentScanFileSize: maxContentScan,
			MaxArchiveFileSize:     maxArchiveFile,
			MaxTotalArchiveMemory:  maxTotalArchiveMem,
		},
		Rules: []config.RuleSpec{{
			Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true,
			Params: []map[string]interface{}{
				{
					"keywords": []string{"password"},
					"info":     "Possible credentials in file",
				},
			},
		}},
	}
}

func TestOOXMLGateEmitsSkipAck(t *testing.T) {
	// A container whose zip index declares far more than the limits allow is
	// rejected before parsing, with an acknowledgement.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "xl/worksheets/sheet1.xml",
		Method:             zip.Deflate,
		UncompressedSize64: 1 << 40,
		CompressedSize64:   8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{0x07, 0xff, 0xff, 0xff, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bomb.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := newKeywordConfig(1024*1024*1024, 10*1024*1024, 100*1024*1024)
	msgs := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, structs.File{Path: path, Name: "bomb.xlsx"})
	found := false
	for _, m := range msgs {
		if m.Skipped && strings.Contains(m.Content, "container declares more data") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected declared-size gate ack, got %v", msgs)
	}
}

func TestOOXMLParseErrorEmitsSkipAck(t *testing.T) {
	// Valid zip, not a valid xlsx: parse fails AFTER the container opened ->
	// acknowledgement instead of the former silent warning.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("not-an-xlsx.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "odd.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := newKeywordConfig(1024*1024*1024, 10*1024*1024, 100*1024*1024)
	msgs := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, structs.File{Path: path, Name: "odd.xlsx"})
	found := false
	for _, m := range msgs {
		if m.Skipped && strings.Contains(m.Content, "could not be parsed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected parse-error ack, got %v", msgs)
	}
}

func TestOOXMLMisnamedTextFileStillScanned(t *testing.T) {
	// A CSV misnamed .xlsx does not open as zip -> falls through to the text
	// flow and its content keeps being keyword-scanned (no silent coverage loss).
	path := filepath.Join(t.TempDir(), "data.xlsx")
	if err := os.WriteFile(path, []byte("col1,col2\npassword,value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := newKeywordConfig(1024*1024*1024, 10*1024*1024, 100*1024*1024)
	msgs := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, structs.File{Path: path, Name: "data.xlsx"})
	found := false
	for _, m := range msgs {
		if !m.Skipped && strings.Contains(m.Content, "Possible credentials") {
			found = true
		}
	}
	if !found {
		t.Errorf("misnamed text file must still be keyword-scanned, got %v", msgs)
	}
}

func TestIsFreeOfKeywords_OversizedFileEmitsSkipMessage(t *testing.T) {
	path := tempFile([]byte("password is hunter2 and more text"))
	defer os.Remove(path)

	file := structs.File{Path: path, Name: "big.txt", DisplayName: "big.txt"}

	// MaxContentScanFileSize of 1 byte forces the size-skip branch.
	cfg := newKeywordConfig(1, 10*1024*1024, 100*1024*1024)

	messages := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, file)

	if len(messages) != 1 {
		t.Fatalf("expected exactly 1 skip message, got %d: %+v", len(messages), messages)
	}
	m := messages[0]
	if !m.Skipped {
		t.Errorf("expected Skipped=true")
	}
	if m.Reason == "" {
		t.Errorf("expected non-empty Reason")
	}
	if !strings.Contains(m.Content, "exceeds maximum") {
		t.Errorf("unexpected skip content: %q", m.Content)
	}
	if src, ok := m.Source.(structs.File); !ok || src.Path != path {
		t.Errorf("expected File source with path %q, got %+v", path, m.Source)
	}
}

func TestIsFreeOfKeywords_NormalFileNotSkipped(t *testing.T) {
	path := tempFile([]byte("password is hunter2"))
	defer os.Remove(path)

	file := structs.File{Path: path, Name: "ok.txt", DisplayName: "ok.txt"}

	// Generous limit: content scan proceeds, no skip message expected.
	cfg := newKeywordConfig(1024*1024*1024, 10*1024*1024, 100*1024*1024)

	messages := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, file)

	// The scan must NOT have been skipped...
	for _, m := range messages {
		if m.Skipped {
			t.Errorf("did not expect any skip message for a normally-sized file, got %q", m.Content)
		}
	}

	// ...and it must have actually RUN: the planted "password" keyword
	// (configured in newKeywordConfig) must be reported. Asserting the positive
	// finding catches a regression where the file is silently not scanned (no
	// skip message, but also no findings - which the old test would have passed).
	var found bool
	for _, m := range messages {
		if !m.Skipped && strings.Contains(m.Content, "password") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected the planted keyword 'password' to be reported, proving the content scan ran; got messages: %+v", messages)
	}
}

func TestIsArchiveFreeOfKeywords_OversizedArchiveEmitsSkipMessage(t *testing.T) {
	// Use a real archive but set MaxContentScanFileSize below its size so the
	// archive-too-large branch fires.
	archivePath := "../../testdata/archives/complex_archive.zip"
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Skipf("test archive not available: %v", err)
	}

	file := structs.File{Path: archivePath, Name: "complex_archive.zip", DisplayName: "complex_archive.zip", IsArchive: true}

	cfg := newKeywordConfig(info.Size()-1, 10*1024*1024, 100*1024*1024)

	messages := runRule(t, "IsFreeOfKeywords", cfg, ScopeArchiveMember, file)

	if len(messages) != 1 {
		t.Fatalf("expected exactly 1 archive skip message, got %d: %+v", len(messages), messages)
	}
	if !messages[0].Skipped {
		t.Errorf("expected Skipped=true for oversized archive")
	}
	if !strings.Contains(messages[0].Content, "archive") {
		t.Errorf("unexpected archive skip content: %q", messages[0].Content)
	}
}

func TestIsArchiveFreeOfKeywords_MemberSkipsEmitMessages(t *testing.T) {
	archivePath := "../../testdata/archives/complex_archive.zip"
	if _, err := os.Stat(archivePath); err != nil {
		t.Skipf("test archive not available: %v", err)
	}

	file := structs.File{Path: archivePath, Name: "complex_archive.zip", DisplayName: "complex_archive.zip", IsArchive: true}

	// Tiny per-member size limit and tiny total-memory budget force member skips
	// while the archive itself is still under MaxContentScanFileSize.
	cfg := newKeywordConfig(1024*1024*1024, 4, 16)

	messages := runRule(t, "IsFreeOfKeywords", cfg, ScopeArchiveMember, file)

	skipCount := 0
	for _, m := range messages {
		if m.Skipped {
			skipCount++
			if src, ok := m.Source.(structs.File); !ok || src.ArchiveName != "complex_archive.zip" {
				t.Errorf("expected member skip Source to reference archive, got %+v", m.Source)
			}
		}
	}
	if skipCount == 0 {
		t.Errorf("expected at least one archive-member skip message, got none (messages: %+v)", messages)
	}
}

// streamTestFile writes a text file of the given size into t.TempDir, filled
// with keyword-free filler lines, then copies each plant over its offset. Files
// larger than streamChunkSize (the production chunk size and streaming
// threshold) take the streamed scan path; the first chunk boundary the tests
// plant keywords around sits exactly at streamChunkSize.
func streamTestFile(t *testing.T, size int, plants map[int]string) structs.File {
	t.Helper()
	line := []byte("2026-08-11 sensor=alpha depth_m=12.5 temperature_c=8.71 status=ok\n")
	content := make([]byte, size)
	for i := 0; i < size; i += len(line) {
		copy(content[i:], line)
	}
	for offset, plant := range plants {
		copy(content[offset:], plant)
	}
	path := filepath.Join(t.TempDir(), "series.txt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write stream fixture: %v", err)
	}
	return structs.File{Path: path, Name: "series.txt", DisplayName: "series.txt"}
}

// TestIsFreeOfKeywords_StreamedLargeFile pins the streamed acquisition for text
// files past the streaming threshold: the chunk/overlap loop must not lose a keyword
// on a chunk boundary, the cross-chunk dedup must collapse repeats of the SAME
// finding without collapsing DISTINCT ones, and findings must carry the FILE's
// casing, matching the whole-file path.
func TestIsFreeOfKeywords_StreamedLargeFile(t *testing.T) {
	// 512KB past the streaming threshold, read as two chunks.
	const fileSize = streamChunkSize + 512*1024
	tests := []struct {
		name     string
		keywords []string
		plants   map[int]string
		expect   []string // non-skipped message contents, as a multiset
	}{
		{
			// Chunk 1 ends with "pass", the 2KB overlap replays it in front of
			// chunk 2 where the full keyword matches - exactly once.
			name:     "keyword straddling the first chunk boundary is found once",
			keywords: []string{"password"},
			plants:   map[int]string{streamChunkSize - 4: "password"},
			expect:   []string{"Keywords found: 'password'"},
		},
		{
			name:     "keyword entirely inside a later chunk is found",
			keywords: []string{"password"},
			plants:   map[int]string{streamChunkSize + 200*1024: "password"},
			expect:   []string{"Keywords found: 'password'"},
		},
		{
			// Dedup keys on rule + lowered content: the same keyword in two
			// chunks is ONE finding, reported once.
			name:     "same keyword in two chunks deduplicates to one message",
			keywords: []string{"password"},
			plants:   map[int]string{1000: "password", streamChunkSize + 200*1024: "password"},
			expect:   []string{"Keywords found: 'password'"},
		},
		{
			// Distinct keywords are distinct dedup keys; both survive. The
			// whole-file path would join these into one message, so two
			// messages also prove the streamed path ran.
			name:     "two different keywords are both reported",
			keywords: []string{"password", "secretkey"},
			plants:   map[int]string{1000: "password", streamChunkSize + 200*1024: "secretkey"},
			expect:   []string{"Keywords found: 'password'", "Keywords found: 'secretkey'"},
		},
		{
			// Matching is case-insensitive and the message carries the file's
			// spelling, consistent with the <=1MB path.
			name:     "uppercased keyword matches and keeps the file's casing",
			keywords: []string{"password"},
			plants:   map[int]string{streamChunkSize + 100*1024: "PASSWORD"},
			expect:   []string{"Keywords found: 'PASSWORD'"},
		},
		{
			// Casings of one keyword share a lowered dedup key: one finding,
			// reported with the casing of the chunk it was first found in.
			name:     "differently cased repeats collapse to the first chunk's casing",
			keywords: []string{"admin"},
			plants:   map[int]string{1000: "Admin", streamChunkSize + 200*1024: "ADMIN"},
			expect:   []string{"Keywords found: 'Admin'"},
		},
		{
			name:     "large file without keywords yields no message",
			keywords: []string{"password"},
			plants:   nil,
			expect:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := streamTestFile(t, fileSize, tt.plants)
			cfg := config.Config{
				General: &config.GeneralConfig{MaxContentScanFileSize: 1024 * 1024 * 1024},
				Rules: []config.RuleSpec{{
					Name: "IsFreeOfKeywords", Check: "IsFreeOfKeywords", Enabled: true,
					Params: []map[string]interface{}{
						{"keywords": tt.keywords, "info": "Keywords found:"},
					},
				}},
			}

			messages := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, file)

			var got []string
			for _, m := range messages {
				if m.Skipped {
					t.Fatalf("unexpected skip message: %q", m.Content)
				}
				got = append(got, m.Content)
			}
			sort.Strings(got)
			want := append([]string(nil), tt.expect...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("expected %d messages %v, got %v", len(want), want, got)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("expected message %v, got %v", want[i], got[i])
				}
			}
		})
	}
}

func TestIsFreeOfKeywords_BinaryFileEmitsSkipMessage(t *testing.T) {
	// Null bytes make isTextFile report a binary file; the filename has no
	// supported-archive extension, so the binary-skip branch fires.
	path := tempFile([]byte{0x00, 0x01, 0x02, 0x00, 0xff, 0xfe})
	defer os.Remove(path)

	file := structs.File{Path: path, Name: "blob.bin", DisplayName: "blob.bin"}

	// Generous size limit so the size-skip branch does not fire.
	cfg := newKeywordConfig(1024*1024*1024, 10*1024*1024, 100*1024*1024)

	messages := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, file)

	skipCount := 0
	for _, m := range messages {
		if m.Skipped {
			skipCount++
			if m.Reason != "Binary file detected" {
				t.Errorf("expected reason 'Binary file detected', got %q", m.Reason)
			}
			if src, ok := m.Source.(structs.File); !ok || src.Path != path {
				t.Errorf("expected File source with path %q, got %+v", path, m.Source)
			}
		}
	}
	if skipCount != 1 {
		t.Errorf("expected exactly 1 binary skip message, got %d (messages: %+v)", skipCount, messages)
	}
}

// TestKeywordFindingsCarryTheScannedFile pins the attribution of a keyword
// finding over every acquisition that stamps one: the scan reports without a
// Source - so one box serves every parameter set of every rule - and the
// acquisition stamps the file it read, through sourceAll for the whole-file,
// streamed, OOXML and PDF paths and through its own lazily built member file
// for an archive member. Drop the stamp from one acquisition and its case
// reports a finding no renderer can attribute.
func TestKeywordFindingsCarryTheScannedFile(t *testing.T) {
	textPath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(textPath, []byte("the password lives here, hunter2 too, and secretkey as well\n"), 0o600); err != nil {
		t.Fatalf("write text fixture: %v", err)
	}
	// Several scans over ONE acquisition: two parameter sets of one rule and a
	// second rule beside it. Every finding must carry the box the first one
	// made, so a stamp that only reaches the first scan's findings fails here.
	shared := keywordConfig([]string{"password"})
	shared.Rules[0].Name = "credentials"
	shared.Rules[0].Params = append(shared.Rules[0].Params, map[string]interface{}{
		"keywords": []string{"hunter2"},
		"info":     "A second parameter set found:",
	})
	shared.Rules = append(shared.Rules, config.RuleSpec{
		Name: "internals", Check: "IsFreeOfKeywords", Enabled: true,
		Params: []map[string]interface{}{
			{"keywords": []string{"secretkey"}, "info": "A second rule found:"},
		},
	})

	tests := []struct {
		name     string
		file     structs.File
		scope    Scope
		cfg      config.Config
		findings int // how many findings the fixture yields, so a silent miss fails
	}{
		{
			name:     "whole text file, two rules and three parameter sets",
			file:     structs.File{Path: textPath, Name: "notes.txt"},
			scope:    ScopeFile,
			cfg:      shared,
			findings: 3,
		},
		{
			name:     "streamed text file",
			file:     streamTestFile(t, streamChunkSize+512*1024, map[int]string{1000: "password"}),
			scope:    ScopeFile,
			cfg:      keywordConfig([]string{"password"}),
			findings: 1,
		},
		{
			name:     "OOXML container",
			file:     structs.File{Path: "../../testdata/test.xlsx", Name: "test.xlsx"},
			scope:    ScopeFile,
			cfg:      keywordConfig([]string{"column2"}),
			findings: 1,
		},
		{
			name:     "PDF",
			file:     writePDFFixture(t, buildTestPDF("the password lives here")),
			scope:    ScopeFile,
			cfg:      keywordConfig([]string{"password"}),
			findings: 1,
		},
		{
			// The member file the walk builds, not the archive: its Path is
			// still the archive's, which is what the assertion below reads.
			name:     "archive member",
			file:     structs.File{Path: "../../testdata/archives/one_of_each.zip", Name: "one_of_each.zip", DisplayName: "one_of_each.zip", IsArchive: true},
			scope:    ScopeArchiveMember,
			cfg:      keywordConfig([]string{"password"}),
			findings: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			findings := 0
			for _, m := range runRule(t, "IsFreeOfKeywords", tt.cfg, tt.scope, tt.file) {
				if m.Skipped {
					continue
				}
				findings++
				src, ok := m.Source.(structs.File)
				if !ok {
					t.Fatalf("finding %q carries no file source: %+v", m.Content, m.Source)
				}
				if src.Path != tt.file.Path {
					t.Errorf("finding %q is sourced at %q, want the scanned file %q", m.Content, src.Path, tt.file.Path)
				}
			}
			if findings != tt.findings {
				t.Fatalf("%d findings over %q, want %d: the source assertions above did not run over what this case scans", findings, tt.file.Path, tt.findings)
			}
		})
	}
}

// TestLowerIntoMatchesBytesToLower pins lowerInto's contract: byte-identical
// to bytes.ToLower through ONE shared scratch reused across successive calls,
// as streamChunks reuses it across chunks.
func TestLowerIntoMatchesBytesToLower(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"pure ASCII", "the quick brown fox 0123456789 jumps"},
		{"ASCII uppercase at boundaries", "Abc mIxEd caSE xyZ"},
		{"umlauts", "Grüße aus Dübendorf, Öl und Äpfel"},
		{"degree and micro", "25.3°C at 4.7µm"},
		{"dotted capital I U+0130", "İstanbul İİ"},
		{"kelvin sign U+212A", "273.15K and K"},
		{"titlecase Dz U+01C5", "ǅeno ǅ"},
		{"capital sigma", "ΣΙΣΥΦΟΣ Σ"},
		{"invalid single byte", "abc\xffdef"},
		{"invalid byte pair", "abc\xfe\xffdef"},
		{"truncated multi-byte at end", "grüße\xc3"},
		{"empty", ""},
	}

	scratch := make([]byte, 0, 8) // deliberately small: forces growth via append
	for _, tc := range cases {
		src := []byte(tc.src)
		want := bytes.ToLower(src)
		scratch = lowerInto(scratch, src)
		if !bytes.Equal(scratch, want) {
			t.Errorf("%s: lowerInto = %q, want %q", tc.name, scratch, want)
		}
	}
}

// TestLowerIntoRandomizedMatchesBytesToLower cross-checks lowerInto against
// bytes.ToLower on random mixes of ASCII, valid UTF-8 and garbage bytes, and
// guards against stale-tail leftovers by following every random call with a
// pure-ASCII call on the same scratch.
func TestLowerIntoRandomizedMatchesBytesToLower(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	scratch := make([]byte, 0, 8)
	asciiProbe := []byte("Stale TAIL Probe 0123")
	probeWant := bytes.ToLower(asciiProbe)

	for i := 0; i < 3000; i++ {
		n := rng.Intn(200)
		src := make([]byte, 0, n*3)
		for len(src) < n {
			switch rng.Intn(3) {
			case 0: // ASCII byte
				src = append(src, byte(rng.Intn(utf8.RuneSelf)))
			case 1: // valid UTF-8 rune (surrogates encode as U+FFFD)
				src = utf8.AppendRune(src, rune(rng.Intn(utf8.MaxRune+1)))
			default: // raw byte, often invalid UTF-8
				src = append(src, byte(rng.Intn(256)))
			}
		}
		want := bytes.ToLower(src)
		scratch = lowerInto(scratch, src)
		if !bytes.Equal(scratch, want) {
			t.Fatalf("iteration %d: lowerInto(%q) = %q, want %q", i, src, scratch, want)
		}
		scratch = lowerInto(scratch, asciiProbe)
		if !bytes.Equal(scratch, probeWant) {
			t.Fatalf("iteration %d: stale tail after %q: got %q, want %q", i, src, scratch, probeWant)
		}
	}
}
