package readers

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

func TestIsTransientReadError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"wrapped path error", fmt.Errorf("scan: %w", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}), true},
		{"missing file", &fs.PathError{Op: "stat", Path: "x", Err: syscall.ENOENT}, true},
		{"directory read as a file", &fs.PathError{Op: "read", Path: "x", Err: syscall.EISDIR}, false},
		{"non-directory in the path", &fs.PathError{Op: "stat", Path: "x", Err: syscall.ENOTDIR}, false},
		{"name too long", &fs.PathError{Op: "open", Path: "x", Err: syscall.ENAMETOOLONG}, false},
		{"symlink loop", &fs.PathError{Op: "open", Path: "x", Err: syscall.ELOOP}, false},
		{"format error", zip.ErrFormat, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		if got := IsTransientReadError(tt.err); got != tt.want {
			t.Errorf("%s: IsTransientReadError = %v, want %v", tt.name, got, tt.want)
		}
	}
}
