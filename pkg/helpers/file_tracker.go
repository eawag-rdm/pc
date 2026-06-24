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

// Reset clears the tracked files so the tracker can be reused. The server calls
// this at the top of each request because PDFTracker is process-global and would
// otherwise leak PDF notes across requests.
func (ft *FileTracker) Reset() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.Files = make([]string, 0)
}

// SnapshotFiles returns a copy of the tracked files taken under the tracker's
// lock. Callers (e.g. the server building a response body) must use this rather
// than reading ft.Files directly: ft.Files is mutated under ft.mu by Reset and
// AddFileIfPDF, so an unsynchronized field read would be a data race.
func (ft *FileTracker) SnapshotFiles() []string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]string(nil), ft.Files...)
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
