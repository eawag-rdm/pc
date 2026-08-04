package collectors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
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
// depthTestConfig builds a LocalCollector config with the given attrs.
func depthTestConfig(attrs map[string]interface{}) config.Config {
	return config.Config{Collectors: map[string]*config.CollectorConfig{"LocalCollector": {Attrs: attrs}}}
}

// depthTestTree creates root/f0.txt, root/d1/f1.txt, root/d1/d2/f2.txt.
func depthTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d1", "d2"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"f0.txt", "d1/f1.txt", "d1/d2/f2.txt"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func collectedNames(files []structs.File) []string {
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

func TestLocalCollectorFolderDepth(t *testing.T) {
	root := depthTestTree(t)

	t.Run("depth 0 is top-level only", func(t *testing.T) {
		files, err := LocalCollector(root, depthTestConfig(map[string]interface{}{"maxFolderDepth": int64(0)}))
		if err != nil {
			t.Fatal(err)
		}
		if got := collectedNames(files); len(got) != 1 || got[0] != "f0.txt" {
			t.Errorf("expected [f0.txt], got %v", got)
		}
	})

	t.Run("depth 1 lists boundary dir but does not descend", func(t *testing.T) {
		files, err := LocalCollector(root, depthTestConfig(map[string]interface{}{"maxFolderDepth": int64(1)}))
		if err != nil {
			t.Fatal(err)
		}
		got := collectedNames(files)
		want := []string{"d1", "f1.txt", "d2", "f0.txt"}
		for _, w := range want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				t.Errorf("expected %q in %v", w, got)
			}
		}
		for _, g := range got {
			if g == "f2.txt" {
				t.Errorf("f2.txt is below the depth limit, got %v", got)
			}
		}
	})

	t.Run("explicit depth wins over includeFolders", func(t *testing.T) {
		files, err := LocalCollector(root, depthTestConfig(map[string]interface{}{"includeFolders": true, "maxFolderDepth": int64(0)}))
		if err != nil {
			t.Fatal(err)
		}
		if got := collectedNames(files); len(got) != 1 || got[0] != "f0.txt" {
			t.Errorf("expected [f0.txt], got %v", got)
		}
	})

	t.Run("legacy includeFolders stays unlimited", func(t *testing.T) {
		files, err := LocalCollector(root, depthTestConfig(map[string]interface{}{"includeFolders": true}))
		if err != nil {
			t.Fatal(err)
		}
		got := collectedNames(files)
		found := false
		for _, g := range got {
			if g == "f2.txt" {
				found = true
			}
		}
		if !found {
			t.Errorf("legacy includeFolders must keep unlimited recursion, got %v", got)
		}
	})

	t.Run("negative depth is an error", func(t *testing.T) {
		_, err := LocalCollector(root, depthTestConfig(map[string]interface{}{"maxFolderDepth": int64(-1)}))
		if err == nil {
			t.Error("expected error for negative maxFolderDepth")
		}
	})
}

func TestLocalCollectorMaxFileCount(t *testing.T) {
	root := depthTestTree(t)
	files, err := LocalCollector(root, depthTestConfig(map[string]interface{}{"includeFolders": true, "maxFileCount": int64(2)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("expected exactly 2 collected entries, got %v", collectedNames(files))
	}
}

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
