package checks

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// keywordConfig builds a config that scans for the given literal keywords.
func keywordConfig(keywords []string) config.Config {
	return config.Config{
		General: &config.GeneralConfig{
			MaxContentScanFileSize: 1024 * 1024 * 1024,
			MaxArchiveFileSize:     10 * 1024 * 1024,
			MaxTotalArchiveMemory:  100 * 1024 * 1024,
			MaxArchiveMemberCount:  1000,
		},
		Tests: map[string]*config.TestConfig{
			"IsFreeOfKeywords": {
				KeywordArguments: []map[string]interface{}{
					{"keywords": keywords, "info": "Sensitive data found:"},
				},
			},
		},
	}
}

// The fixtures carry known text: test.xlsx extracts "row1 column2 \nrow2 \n",
// test.docx extracts "PAGE 1" plus a table.

func TestKeywordDetectedInTopLevelOOXML(t *testing.T) {
	tests := []struct {
		fixture string
		keyword string
	}{
		{"../../testdata/test.xlsx", "column2"},
		{"../../testdata/test.docx", "page"}, // matches "PAGE 1" case-insensitively
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.fixture), func(t *testing.T) {
			cfg := keywordConfig([]string{tt.keyword})
			file := structs.File{Path: tt.fixture, Name: filepath.Base(tt.fixture)}
			msgs := runRule(t, "IsFreeOfKeywords", cfg, ScopeFile, file)
			found := false
			for _, m := range msgs {
				if !m.Skipped && strings.Contains(strings.ToLower(m.Content), tt.keyword) {
					found = true
				}
			}
			if !found {
				t.Errorf("keyword %q must be detected in %s, got %v", tt.keyword, tt.fixture, msgs)
			}

			// Negative control: an absent keyword yields no findings.
			none := runRule(t, "IsFreeOfKeywords", keywordConfig([]string{"zzz-not-present"}), ScopeFile, file)
			for _, m := range none {
				if !m.Skipped {
					t.Errorf("no finding expected for absent keyword, got %v", m)
				}
			}
		})
	}
}

// buildArchiveWithOOXML packs both fixtures plus a decoy into zip/tar/tar.gz.
func buildArchiveWithOOXML(t *testing.T, format string) structs.File {
	t.Helper()
	xlsxData, err := os.ReadFile("../../testdata/test.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	docxData, err := os.ReadFile("../../testdata/test.docx")
	if err != nil {
		t.Fatal(err)
	}
	members := []struct {
		name string
		data []byte
	}{
		{"report.xlsx", xlsxData},
		{"notes.docx", docxData},
		{"decoy.txt", []byte("nothing to see here\n")},
	}

	var buf bytes.Buffer
	switch format {
	case "zip":
		zw := zip.NewWriter(&buf)
		for _, m := range members {
			w, err := zw.Create(m.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(m.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	case "tar", "tar.gz":
		var out io.Writer = &buf
		var gw *gzip.Writer
		if format == "tar.gz" {
			gw = gzip.NewWriter(&buf)
			out = gw
		}
		tw := tar.NewWriter(out)
		for _, m := range members {
			if err := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o600, Size: int64(len(m.data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(m.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if gw != nil {
			if err := gw.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}

	name := "package." + format
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return structs.File{Path: path, Name: name, IsArchive: true}
}

func TestKeywordDetectedInArchivedOOXML(t *testing.T) {
	for _, format := range []string{"zip", "tar", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			archive := buildArchiveWithOOXML(t, format)

			// One keyword per container format, both must be found.
			cfg := keywordConfig([]string{"column2", "page"})
			msgs := runRule(t, "IsFreeOfKeywords", cfg, ScopeArchiveMember, archive)

			foundXLSX, foundDOCX := false, false
			for _, m := range msgs {
				if m.Skipped {
					continue
				}
				src, ok := m.Source.(structs.File)
				if !ok {
					continue
				}
				lower := strings.ToLower(m.Content)
				if src.Name == "report.xlsx" && strings.Contains(lower, "column2") {
					foundXLSX = true
					if src.ArchiveName == "" {
						t.Errorf("xlsx finding must be attributed to the archive, got %+v", src)
					}
				}
				if src.Name == "notes.docx" && strings.Contains(lower, "page") {
					foundDOCX = true
				}
			}
			if !foundXLSX {
				t.Errorf("[%s] keyword in archived xlsx must be detected, got %v", format, msgs)
			}
			if !foundDOCX {
				t.Errorf("[%s] keyword in archived docx must be detected, got %v", format, msgs)
			}

			// Negative control: absent keyword yields no findings at all.
			none := runRule(t, "IsFreeOfKeywords", keywordConfig([]string{"zzz-not-present"}), ScopeArchiveMember, archive)
			for _, m := range none {
				if !m.Skipped {
					t.Errorf("[%s] no finding expected for absent keyword, got %v", format, m)
				}
			}
		})
	}
}
