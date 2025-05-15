package structs

import (
	"os"
	"path"
	"path/filepath"
)

type Repository struct {
	Files []File
}

type File struct {
	Path      string
	Name      string
	Size      int64
	Suffix    string
	IsArchive bool
}

func GetFileSize(file string) int64 {
	fi, err := os.Stat(file)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func ToFile(fpath string, name string, size int64, suffix string) File {
	if fpath == "" {
		panic("file path cannot be empty")
	}
	if name == "" {
		name = path.Base(fpath)
	}
	if size == -1 {
		size = GetFileSize(fpath)
	}
	if suffix == "" {
		suffix = filepath.Ext(name)
	}
	isArchive := false
	ext := path.Ext(name)
	if ext == ".zip" || ext == ".tar" || ext == ".gz" || ext == ".7z" {
		isArchive = true
	}
	return File{Path: fpath, Name: name, Size: size, Suffix: suffix, IsArchive: isArchive}
}
