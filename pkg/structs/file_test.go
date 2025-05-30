package structs

import (
	"testing"
)

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
			want:   File{Path: "/path/to/file.txt", Name: "file.txt", Size: 0, Suffix: ".txt", IsArchive: false},
		},
		{
			fpath:  "/path/to/file.txt",
			name:   "custom_name.txt",
			size:   1234,
			suffix: ".txt",
			want:   File{Path: "/path/to/file.txt", Name: "custom_name.txt", Size: 1234, Suffix: ".txt", IsArchive: false},
		},
		{
			fpath:  "/path/to/file",
			name:   "",
			size:   0,
			suffix: "",
			want:   File{Path: "/path/to/file", Name: "file", Size: 0, Suffix: "", IsArchive: false},
		},
		{
			fpath:  "/path/to/file.zip",
			name:   "file.zip",
			size:   0,
			suffix: "",
			want:   File{Path: "/path/to/file.zip", Name: "file.zip", Size: 0, Suffix: ".zip", IsArchive: true},
		},
	}

	for _, tt := range tests {
		got := ToFile(tt.fpath, tt.name, tt.size, tt.suffix)
		if got.Path != tt.want.Path || got.Name != tt.want.Name || got.Size != tt.want.Size || got.Suffix != tt.want.Suffix || got.IsArchive != tt.want.IsArchive {
			t.Errorf("newFile(%q, %q, %d, %q) = %+v; want %+v", tt.fpath, tt.name, tt.size, tt.suffix, got, tt.want)
		}
	}
}
