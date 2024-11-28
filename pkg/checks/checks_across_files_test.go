package checks

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/utils"
)

func TestIsReadme(t *testing.T) {
	tests := []struct {
		name     string
		file     utils.File
		expected bool
	}{
		{"Test with readme.md", utils.File{Name: "readme.md"}, true},
		{"Test with readme.txt", utils.File{Name: "readme.txt"}, true},
		{"Test with README.MD", utils.File{Name: "README.MD"}, true},
		{"Test with README.TXT", utils.File{Name: "README.TXT"}, true},
		{"Test with other file", utils.File{Name: "otherfile.txt"}, false},
		{"Test with empty file name", utils.File{Name: ""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isReadme(tt.file)
			if result != tt.expected {
				t.Errorf("isReadme(%v) = %v; expected %v", tt.file, result, tt.expected)
			}
		})
	}
}
