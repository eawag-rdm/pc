package helpers

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestNewFileTracker(t *testing.T) {
	header := "Test Header"
	tracker := NewFileTracker(header)

	if tracker == nil {
		t.Fatal("NewFileTracker returned nil")
	}

	if tracker.Header != header {
		t.Errorf("Expected header '%s', got '%s'", header, tracker.Header)
	}

	if tracker.Files == nil {
		t.Error("Files slice not initialized")
	}

	if len(tracker.Files) != 0 {
		t.Errorf("Expected empty files slice, got %d items", len(tracker.Files))
	}
}

func TestFileTracker_AddFileIfPDF_WithSuffix(t *testing.T) {
	tracker := NewFileTracker("Test")

	// Test file with .pdf suffix
	pdfFile := structs.File{
		Name:   "document.pdf",
		Path:   "/path/document.pdf",
		Suffix: ".pdf",
	}

	tracker.AddFileIfPDF("Note: ", pdfFile)

	if len(tracker.Files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(tracker.Files))
	}

	expected := "Note: document.pdf"
	if tracker.Files[0] != expected {
		t.Errorf("Expected '%s', got '%s'", expected, tracker.Files[0])
	}
}

func TestFileTracker_AddFileIfPDF_WithoutSuffix(t *testing.T) {
	tracker := NewFileTracker("Test")

	// Test file with .pdf in name but no suffix field
	pdfFile := structs.File{
		Name:   "document.pdf",
		Path:   "/path/document.pdf",
		Suffix: "", // No suffix set
	}

	tracker.AddFileIfPDF("Note: ", pdfFile)

	if len(tracker.Files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(tracker.Files))
	}

	expected := "Note: document.pdf"
	if tracker.Files[0] != expected {
		t.Errorf("Expected '%s', got '%s'", expected, tracker.Files[0])
	}
}

func TestFileTracker_AddFileIfPDF_NonPDF(t *testing.T) {
	tracker := NewFileTracker("Test")

	// Test non-PDF file
	textFile := structs.File{
		Name:   "document.txt",
		Path:   "/path/document.txt",
		Suffix: ".txt",
	}

	tracker.AddFileIfPDF("Note: ", textFile)

	if len(tracker.Files) != 0 {
		t.Errorf("Expected 0 files for non-PDF, got %d", len(tracker.Files))
	}
}

func TestFileTracker_AddFileIfPDF_CaseInsensitive(t *testing.T) {
	tracker := NewFileTracker("Test")

	// Test PDF with uppercase extension
	pdfFile := structs.File{
		Name:   "document.PDF",
		Path:   "/path/document.PDF",
		Suffix: ".PDF",
	}

	tracker.AddFileIfPDF("", pdfFile)

	// Current implementation is case-sensitive, so this should not match
	if len(tracker.Files) != 0 {
		t.Errorf("Expected 0 files for uppercase PDF (case-sensitive), got %d", len(tracker.Files))
	}
}

func TestFileTracker_AddFileIfPDF_MultipleFiles(t *testing.T) {
	tracker := NewFileTracker("Test")

	files := []structs.File{
		{Name: "doc1.pdf", Suffix: ".pdf"},
		{Name: "doc2.txt", Suffix: ".txt"},
		{Name: "doc3.pdf", Suffix: ".pdf"},
		{Name: "doc4.doc", Suffix: ".doc"},
	}

	for i, file := range files {
		tracker.AddFileIfPDF(fmt.Sprintf("File%d: ", i+1), file)
	}

	if len(tracker.Files) != 2 {
		t.Fatalf("Expected 2 PDF files, got %d", len(tracker.Files))
	}

	// Check specific files were added
	expectedFiles := []string{"File1: doc1.pdf", "File3: doc3.pdf"}
	for i, expected := range expectedFiles {
		if tracker.Files[i] != expected {
			t.Errorf("File %d: expected '%s', got '%s'", i, expected, tracker.Files[i])
		}
	}
}

func TestFileTracker_FormatFiles_WithFiles(t *testing.T) {
	tracker := NewFileTracker("=== PDF Files ===")

	pdfFile := structs.File{Name: "document.pdf", Suffix: ".pdf"}
	tracker.AddFileIfPDF("Found: ", pdfFile)

	formatted := tracker.FormatFiles()

	if !strings.Contains(formatted, "=== PDF Files ===") {
		t.Error("Formatted output should contain header")
	}

	if !strings.Contains(formatted, "Found: document.pdf") {
		t.Error("Formatted output should contain file entry")
	}

	if strings.Contains(formatted, "No files found") {
		t.Error("Should not contain 'No files found' when files exist")
	}
}

func TestFileTracker_FormatFiles_NoFiles(t *testing.T) {
	tracker := NewFileTracker("=== PDF Files ===")

	formatted := tracker.FormatFiles()

	if !strings.Contains(formatted, "=== PDF Files ===") {
		t.Error("Formatted output should contain header")
	}

	if !strings.Contains(formatted, "No files found") {
		t.Error("Should contain 'No files found' when no files exist")
	}
}

func TestFileTracker_FormatFiles_MultipleFiles(t *testing.T) {
	tracker := NewFileTracker("Test Header")

	files := []structs.File{
		{Name: "doc1.pdf", Suffix: ".pdf"},
		{Name: "doc2.pdf", Suffix: ".pdf"},
		{Name: "doc3.pdf", Suffix: ".pdf"},
	}

	for _, file := range files {
		tracker.AddFileIfPDF("", file)
	}

	formatted := tracker.FormatFiles()
	lines := strings.Split(strings.TrimSpace(formatted), "\n")

	// Should have header + 3 files = 4 lines
	if len(lines) != 4 {
		t.Errorf("Expected 4 lines, got %d", len(lines))
	}

	if lines[0] != "Test Header" {
		t.Errorf("First line should be header, got '%s'", lines[0])
	}
}

func TestPDFTracker_GlobalInstance(t *testing.T) {
	// Test that the global PDFTracker is properly initialized
	if PDFTracker == nil {
		t.Fatal("PDFTracker global instance is nil")
	}

	if PDFTracker.Header != "=== PDF Files ===" {
		t.Errorf("Expected header '=== PDF Files ===', got '%s'", PDFTracker.Header)
	}

	if PDFTracker.Files == nil {
		t.Error("PDFTracker Files slice not initialized")
	}
}

func TestFileTracker_EdgeCases(t *testing.T) {
	tracker := NewFileTracker("Test")

	// Test with empty file name
	emptyFile := structs.File{Name: "", Suffix: ".pdf"}
	tracker.AddFileIfPDF("Empty: ", emptyFile)

	if len(tracker.Files) != 1 {
		t.Errorf("Expected 1 file even with empty name, got %d", len(tracker.Files))
	}

	if tracker.Files[0] != "Empty: " {
		t.Errorf("Expected 'Empty: ', got '%s'", tracker.Files[0])
	}

	// Test with file that ends with 'pdf' but not '.pdf'
	tracker2 := NewFileTracker("Test")
	notPdfFile := structs.File{Name: "mypdf", Suffix: ""}
	tracker2.AddFileIfPDF("", notPdfFile)

	if len(tracker2.Files) != 0 {
		t.Errorf("Expected 0 files for name ending in 'pdf' but not '.pdf', got %d", len(tracker2.Files))
	}

	// Test with file that contains .pdf but doesn't end with it
	tracker3 := NewFileTracker("Test")
	containsPdfFile := structs.File{Name: "file.pdf.backup", Suffix: ".backup"}
	tracker3.AddFileIfPDF("", containsPdfFile)

	if len(tracker3.Files) != 0 {
		t.Errorf("Expected 0 files for name containing '.pdf' but not ending with it, got %d", len(tracker3.Files))
	}
}

func TestFileTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewFileTracker("Test")
	var wg sync.WaitGroup
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			file := structs.File{
				Name:   fmt.Sprintf("doc%d.pdf", id),
				Suffix: ".pdf",
			}
			tracker.AddFileIfPDF("", file)
		}(i)
	}

	wg.Wait()

	if len(tracker.Files) != numGoroutines {
		t.Errorf("Expected %d files, got %d", numGoroutines, len(tracker.Files))
	}
}

