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
	Path        string
	Name        string
	DisplayName string // User-friendly name (CKAN resource name or filename)
	Size        int64
	Suffix      string
	IsArchive   bool
	ArchiveName string // Name of parent archive if this file is inside an archive
}

func GetFileSize(file string) int64 {
	fi, err := os.Stat(file)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func ToFile(fpath string, name string, size int64, suffix string) File {
	return ToFileWithDisplay(fpath, name, "", size, suffix, "")
}

// ToFileWithDisplay creates a File with explicit display name and archive context
func ToFileWithDisplay(fpath string, name string, displayName string, size int64, suffix string, archiveName string) File {
	if fpath == "" {
		panic("file path cannot be empty")
	}
	if name == "" {
		name = path.Base(fpath)
	}
	if displayName == "" {
		displayName = name
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
	return File{
		Path:        fpath,
		Name:        name,
		DisplayName: displayName,
		Size:        size,
		Suffix:      suffix,
		IsArchive:   isArchive,
		ArchiveName: archiveName,
	}
}

// GetDisplayName returns the display name, falling back to Name if not set
func (f File) GetDisplayName() string {
	if f.DisplayName != "" {
		return f.DisplayName
	}
	return f.Name
}

