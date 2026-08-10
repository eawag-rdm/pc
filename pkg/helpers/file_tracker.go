package helpers

import (
	"strings"
	"sync"

	"github.com/eawag-rdm/pc/pkg/structs"
)

type FileTracker struct {
	// files is unexported on purpose: it is mutated under mu, so an exported
	// field would be a racy bypass of SnapshotFiles.
	files  []string
	Header string
	mu     sync.Mutex
}

func NewFileTracker(header string) *FileTracker {
	return &FileTracker{
		files:  make([]string, 0),
		Header: header,
	}
}

func (ft *FileTracker) AddFileIfPDF(note string, file structs.File) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if file.Suffix == ".pdf" {
		ft.files = append(ft.files, note+file.Name)
	} else if strings.HasSuffix(file.Name, ".pdf") {
		ft.files = append(ft.files, note+file.Name)
	}
}

// Reset clears the tracked files so the tracker can be reused. The server calls
// this at the top of each request because PDFTracker is process-global and would
// otherwise leak PDF notes across requests.
func (ft *FileTracker) Reset() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.files = make([]string, 0)
}

// SnapshotFiles returns a copy of the tracked files taken under the tracker's
// lock. It is the only way out of the tracker: ft.files is mutated under ft.mu
// by Reset and AddFileIfPDF, so any unsynchronized read would be a data race.
func (ft *FileTracker) SnapshotFiles() []string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]string(nil), ft.files...)
}

var PDFTracker = NewFileTracker("=== PDF Files ===")
