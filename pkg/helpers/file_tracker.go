package helpers

import (
	"strings"
	"sync"

	"github.com/eawag-rdm/pc/pkg/structs"
)

type FileTracker struct {
	Files  []string
	Header string
	mu     sync.Mutex
}

func NewFileTracker(header string) *FileTracker {
	return &FileTracker{
		Files:  make([]string, 0),
		Header: header,
	}
}

func (ft *FileTracker) AddFileIfPDF(note string, file structs.File) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if file.Suffix == ".pdf" {
		ft.Files = append(ft.Files, note+file.Name)
	} else if strings.HasSuffix(file.Name, ".pdf") {
		ft.Files = append(ft.Files, note+file.Name)
	}
}

func (ft *FileTracker) FormatFiles() string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
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
