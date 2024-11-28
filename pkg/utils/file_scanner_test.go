package utils

import (
	"regexp"
	"testing"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		text    string
		want    bool
	}{
		{pattern: "foo", text: "foobar", want: true},
		{pattern: "bar", text: "foobar", want: true},
		{pattern: "baz", text: "foobar", want: false},
		{pattern: "^foo$", text: "foo", want: true},
		{pattern: "^foo$", text: "foobar", want: false},
	}

	for _, tt := range tests {
		r, err := regexp.Compile(tt.pattern)
		if err != nil {
			t.Fatalf("failed to compile pattern %q: %v", tt.pattern, err)
		}
		got := matchPattern(r, tt.text)
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v; want %v", tt.pattern, tt.text, got, tt.want)
		}
	}
}
func TestNewFile(t *testing.T) {
	tests := []struct {
		fpath  string
		name   string
		size   int64
		suffix string
		want   File
	}{
		{
			fpath:  "/path/to/file.txt",
			name:   "",
			size:   0,
			suffix: "",
			want:   File{Path: "/path/to/file.txt", Name: "file.txt", Size: 0, Suffix: ".txt"},
		},
		{
			fpath:  "/path/to/file.txt",
			name:   "custom_name.txt",
			size:   1234,
			suffix: ".txt",
			want:   File{Path: "/path/to/file.txt", Name: "custom_name.txt", Size: 1234, Suffix: ".txt"},
		},
		{
			fpath:  "/path/to/file",
			name:   "",
			size:   0,
			suffix: "",
			want:   File{Path: "/path/to/file", Name: "file", Size: 0, Suffix: ""},
		},
	}

	for _, tt := range tests {
		got := toFile(tt.fpath, tt.name, tt.size, tt.suffix)
		if got.Path != tt.want.Path || got.Name != tt.want.Name || got.Size != tt.want.Size || got.Suffix != tt.want.Suffix {
			t.Errorf("newFile(%q, %q, %d, %q) = %+v; want %+v", tt.fpath, tt.name, tt.size, tt.suffix, got, tt.want)
		}
	}
}
