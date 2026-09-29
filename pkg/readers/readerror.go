package readers

import (
	"errors"
	"io/fs"
	"syscall"
)

// IsTransientReadError reports whether a failed read of a file may succeed on
// a retry: an *fs.PathError in the chain, as os.Open, os.Stat, os.ReadFile,
// File.Read and File.ReadAt return. Format errors (zip.ErrFormat, a bad gzip
// header) are verdicts on the bytes, not failed reads. Neither are the path
// errors the path itself decides - a directory read as a file, a non-directory
// in the path, a name too long, a symlink loop: they fail the same way every
// time.
func IsTransientReadError(err error) bool {
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		return false
	}
	return !errors.Is(err, syscall.EISDIR) &&
		!errors.Is(err, syscall.ENOTDIR) &&
		!errors.Is(err, syscall.ENAMETOOLONG) &&
		!errors.Is(err, syscall.ELOOP)
}
