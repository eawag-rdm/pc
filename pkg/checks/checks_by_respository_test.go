package checks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

// readmeTestConfig builds the REQUIRED HasReadme rule the readme checks read
// their filename list from.
func readmeTestConfig(names ...string) config.Config {
	if len(names) == 0 {
		names = []string{"readme.md", "readme.txt"}
	}
	return config.Config{Rules: []config.RuleSpec{{
		Name: "HasReadme", Check: "HasReadme", Enabled: true,
		Params: []map[string]interface{}{
			{"readme_names": names},
		},
	}}}
}

func TestIsReadme(t *testing.T) {
	names := []string{"readme.md", "readme.txt"}
	tests := []struct {
		name     string
		file     structs.File
		expected bool
	}{
		{"Test with readme.md", structs.File{Name: "readme.md"}, true},
		{"Test with readme.txt", structs.File{Name: "readme.txt"}, true},
		{"Test with README.MD", structs.File{Name: "README.MD"}, true},
		{"Test with README.TXT", structs.File{Name: "README.TXT"}, true},
		{"Test with other file", structs.File{Name: "otherfile.txt"}, false},
		{"Test with empty file name", structs.File{Name: ""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isReadMe(tt.file, names)
			if result != tt.expected {
				t.Errorf("isReadme(%v) = %v; expected %v", tt.file, result, tt.expected)
			}
		})
	}
}

func TestHasReadme_ConfiguredNames(t *testing.T) {
	repo := structs.Repository{Files: []structs.File{
		{Name: "README.rst"},
		{Name: "data.csv"},
	}}

	// Default list does not recognize README.rst.
	if msgs := runRepoRule(t, "HasReadme", readmeTestConfig(), repo); len(msgs) != 1 {
		t.Errorf("expected 1 'no readme' message with default names, got %d", len(msgs))
	}
	// A configured list does.
	if msgs := runRepoRule(t, "HasReadme", readmeTestConfig("readme.rst"), repo); len(msgs) != 0 {
		t.Errorf("expected no message with configured readme.rst, got %d", len(msgs))
	}
}
func TestReadMeContainsTOC(t *testing.T) {
	tests := []struct {
		name          string
		repository    structs.Repository
		expected      []structs.Message
		readmeContent string
	}{
		{
			"Test with complete TOC",
			structs.Repository{
				Files: []structs.File{
					{Name: "readme.md", Path: "testdata/readme_with_toc.md"},
					{Name: "file1.txt"},
					{Name: "file2.txt"},
				},
			},
			nil,
			"# Table of Contents\n\n- file1.txt\n- file2.txt\n",
		},
		{
			"Test incomplete missing TOC",
			structs.Repository{
				Files: []structs.File{
					{Name: "readme.md", Path: "testdata/readme_without_toc.md"},
					{Name: "file1.txt"},
					{Name: "file2.txt"},
				},
			},
			[]structs.Message{{Content: "ReadMe file is missing a complete table of contents for this repository. Missing files are: 'file2.txt'", Source: structs.Repository{Files: []structs.File{{Name: "readme.md", Path: "testdata/readme_without_toc.md"}, {Name: "file1.txt"}, {Name: "file2.txt"}}}}},
			"# Table of Contents\n\n- file1.txt\n",
		},
		{
			"Test with no readme file",
			structs.Repository{
				Files: []structs.File{
					{Name: "file1.txt"},
					{Name: "file2.txt"},
				},
			},
			nil,
			"",
		},
		{
			"Test TOC with files without suffix",
			structs.Repository{
				Files: []structs.File{
					{Name: "readme.md", Path: "testdata/readme_without_toc.md"},
					{Name: "file1.txt"},
					{Name: "file2.txt"},
				},
			},
			[]structs.Message{{Content: "ReadMe file is missing a complete table of contents for this repository. Missing files are: 'file2'", Source: structs.Repository{Files: []structs.File{{Name: "readme.md", Path: "testdata/readme_without_toc.md"}, {Name: "file1.txt"}, {Name: "file2.txt"}}}}},
			"# Table of Contents\n\n- file1\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.readmeContent != "" {
				tempFile, err := os.CreateTemp("", "readme_*.md")
				if err != nil {
					t.Fatalf("Failed to create temporary readme file: %v", err)
				}
				defer os.Remove(tempFile.Name())

				if _, err := tempFile.Write([]byte(tt.readmeContent)); err != nil {
					t.Fatalf("Failed to write to temporary readme file: %v", err)
				}
				if err := tempFile.Close(); err != nil {
					t.Fatalf("Failed to close temporary readme file: %v", err)
				}

				tt.repository.Files[0].Path = tempFile.Name()
			}

			result := runRepoRule(t, "ReadMeContainsTOC", readmeTestConfig(), tt.repository)
			assert.Len(t, result, len(tt.expected))
		})
	}
}

// TestReadMeContainsTOCMissingList pins the message a repository of
// undocumented files produces: the list is capped and the names beyond the cap
// are reported as a count, so the message cannot grow without bound.
func TestReadMeContainsTOCMissingList(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{
			"more missing files than the cap lists",
			[]string{
				"missing01.txt", "missing02.txt", "missing03.txt", "missing04.txt",
				"missing05.txt", "missing06.txt", "missing07.txt", "missing08.txt",
				"missing09.txt", "missing10.txt", "missing11.txt", "missing12.txt",
				"missing13.txt",
			},
			"ReadMe file is missing a complete table of contents for this repository. Missing files are: " +
				"'missing01.txt', 'missing02.txt', 'missing03.txt', 'missing04.txt', 'missing05.txt', " +
				"'missing06.txt', 'missing07.txt', 'missing08.txt', 'missing09.txt', 'missing10.txt' +3 more",
		},
		{
			"a remainder of zero prints nothing",
			[]string{
				"missing01.txt", "missing02.txt", "missing03.txt", "missing04.txt",
				"missing05.txt", "missing06.txt", "missing07.txt", "missing08.txt",
				"missing09.txt", "missing10.txt",
			},
			"ReadMe file is missing a complete table of contents for this repository. Missing files are: " +
				"'missing01.txt', 'missing02.txt', 'missing03.txt', 'missing04.txt', 'missing05.txt', " +
				"'missing06.txt', 'missing07.txt', 'missing08.txt', 'missing09.txt', 'missing10.txt'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The readme names no file, so every one of them is missing.
			readmePath := filepath.Join(t.TempDir(), "readme.md")
			if err := os.WriteFile(readmePath, []byte("# Table of Contents\n"), 0o644); err != nil {
				t.Fatalf("Failed to write readme file: %v", err)
			}
			files := []structs.File{{Name: "readme.md", Path: readmePath}}
			for _, name := range tt.files {
				files = append(files, structs.File{Name: name})
			}

			result := runRepoRule(t, "ReadMeContainsTOC", readmeTestConfig(), structs.Repository{Files: files})

			if len(result) != 1 {
				t.Fatalf("expected exactly 1 message, got %d: %+v", len(result), result)
			}
			if result[0].Content != tt.want {
				t.Errorf("got %q want %q", result[0].Content, tt.want)
			}
		})
	}
}

// TestReadMeContainsTOCUnreadableReadme pins the unreadable-readme path: a
// directory at the readme's path fails os.ReadFile for every user, root
// included, where a mode-0 file would not.
func TestReadMeContainsTOCUnreadableReadme(t *testing.T) {
	readmePath := filepath.Join(t.TempDir(), "readme.md")
	if err := os.Mkdir(readmePath, 0o755); err != nil {
		t.Fatalf("Failed to create directory at readme path: %v", err)
	}

	repository := structs.Repository{Files: []structs.File{
		{Name: "readme.md", Path: readmePath},
		{Name: "file1.txt"},
	}}

	result := runRepoRule(t, "ReadMeContainsTOC", readmeTestConfig(), repository)

	if len(result) != 1 {
		t.Fatalf("expected exactly 1 skip message, got %d: %+v", len(result), result)
	}
	if !result[0].Skipped {
		t.Errorf("expected Skipped=true, got %+v", result[0])
	}
	if result[0].Reason != "Skipped table-of-contents check: the ReadMe could not be read." {
		t.Errorf("unexpected skip reason: %q", result[0].Reason)
	}
	if src, ok := result[0].Source.(structs.File); !ok || src.Path != readmePath {
		t.Errorf("expected File source with path %q, got %+v; a Repository source would leave the skip anonymous downstream", readmePath, result[0].Source)
	}
	if result[0].Content != result[0].Reason {
		t.Errorf("expected Content to repeat the reason %q, got %q", result[0].Reason, result[0].Content)
	}
}
