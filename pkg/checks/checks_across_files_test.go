package checks

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestIsReadme(t *testing.T) {
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
			result := isReadMe(tt.file)
			if result != tt.expected {
				t.Errorf("isReadme(%v) = %v; expected %v", tt.file, result, tt.expected)
			}
		})
	}
}
