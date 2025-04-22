package helpers

import (
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"
)

type FileTracker struct {
	Files  []string
	Header string
}

func NewFileTracker(header string) *FileTracker {
	return &FileTracker{
		Files:  make([]string, 0),
		Header: header,
	}
}

func (ft *FileTracker) AddFileIfPDF(note string, file structs.File) {
	if file.Suffix == ".pdf" {
		ft.Files = append(ft.Files, note+file.Name)
	} else if strings.HasSuffix(file.Name, ".pdf") {
		ft.Files = append(ft.Files, note+file.Name)
	}
}

func (ft *FileTracker) FormatFiles() string {
	var sb strings.Builder
	sb.WriteString(ft.Header + "\n")
	noFilesFound := true
	for _, fileInfo := range ft.Files {
		noFilesFound = false
		sb.WriteString(fileInfo + "\n")
	}
	if noFilesFound {
		sb.WriteString("No files found.\n")
	}
	return sb.String()
}

var PDFTracker = NewFileTracker("=== PDF Files ===")
