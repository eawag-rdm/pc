package utils

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
)

func matchPattern(r *regexp.Regexp, text string) bool {
	return r.MatchString(text)
}

type File struct {
	Path   string
	Name   string
	Size   int64
	Suffix string
}

func getFileSize(file string) int64 {
	fi, err := os.Stat(file)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func toFile(fpath string, name string, size int64, suffix string) File {
	if fpath == "" {
		panic("file path cannot be empty")
	}
	if name == "" {
		name = path.Base(fpath)
	}
	if size == 0 {
		size = getFileSize(fpath)
	}
	if suffix == "" {
		suffix = filepath.Ext(name)
	}
	return File{Path: fpath, Name: name, Size: size, Suffix: suffix}
}
