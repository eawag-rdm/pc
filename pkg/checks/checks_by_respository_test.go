package checks

import (
	"os"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

// readmeTestConfig builds the REQUIRED [test.HasReadme] section the readme
// checks read their filename list from.
func readmeTestConfig(names ...string) config.Config {
	if len(names) == 0 {
		names = []string{"readme.md", "readme.txt"}
	}
	return config.Config{Tests: map[string]*config.TestConfig{
		"HasReadme": {KeywordArguments: []map[string]interface{}{
			{"readme_names": names},
		}},
	}}
}

func TestIsReadme(t *testing.T) {
	names := readmeNames(readmeTestConfig())
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
	if msgs := HasReadme(repo, readmeTestConfig()); len(msgs) != 1 {
		t.Errorf("expected 1 'no readme' message with default names, got %d", len(msgs))
	}
	// A configured list does.
	if msgs := HasReadme(repo, readmeTestConfig("readme.rst")); len(msgs) != 0 {
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

			result := ReadMeContainsTOC(tt.repository, readmeTestConfig())
			assert.Len(t, result, len(tt.expected))
		})
	}
}
