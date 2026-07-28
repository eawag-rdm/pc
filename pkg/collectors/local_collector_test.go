package collectors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

func TestLocalCollector(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "localcollector_test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create some temporary files
	files := []struct {
		name string
		size int64
	}{
		{"file1.txt", 100},
		{"file2.txt", 200},
	}

	for _, file := range files {
		path := filepath.Join(tempDir, file.name)
		err := os.WriteFile(path, make([]byte, file.size), 0644)
		if err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
	}

	// Call the LocalCollector function
	collectedFiles, err := LocalCollector(tempDir, config.Config{Collectors: map[string]*config.CollectorConfig{"LocalCollector": {Attrs: map[string]interface{}{"includeFolders": "false"}}}})
	if err != nil {
		t.Fatalf("LocalCollector returned an error: %v", err)
	}

	// Check the number of collected files
	if len(collectedFiles) != len(files) {
		t.Fatalf("Expected %d files, got %d", len(files), len(collectedFiles))
	}

	// Check the details of the collected files
	for i, file := range files {
		if collectedFiles[i].Name != file.name {
			t.Errorf("Expected file name %s, got %s", file.name, collectedFiles[i].Name)
		}
		if collectedFiles[i].Size != file.size {
			t.Errorf("Expected file size %d, got %d", file.size, collectedFiles[i].Size)
		}
	}
}

// TestLocalCollectorMissingSection ensures LocalCollector still runs without
// panicking when the [collector.LocalCollector] section is absent (nil entry).
// includeFolders defaults to false and files are still collected.
func TestLocalCollectorMissingSection(t *testing.T) {
	tempDir := t.TempDir()

	files := []string{"file1.txt", "file2.txt"}
	for _, name := range files {
		path := filepath.Join(tempDir, name)
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
	}

	// Config has no "LocalCollector" entry at all.
	cfg := config.Config{Collectors: map[string]*config.CollectorConfig{}}

	collectedFiles, err := LocalCollector(tempDir, cfg)
	if err != nil {
		t.Fatalf("LocalCollector returned an error: %v", err)
	}

	if len(collectedFiles) != len(files) {
		t.Fatalf("Expected %d files, got %d", len(files), len(collectedFiles))
	}
}
