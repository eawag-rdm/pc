package utils

import (
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestArchiveParallelProcessing tests that all archives of the same type
// (with different extensions) are processed and their messages are preserved.
// This test ensures the message truncation bug doesn't regress.
func TestArchiveParallelProcessing(t *testing.T) {
	// Create test configuration similar to production
	cfg := config.Config{
		General: &config.GeneralConfig{
			MaxArchiveFileSize:     10485760,  // 10MB
			MaxTotalArchiveMemory:  536870912, // 500MB
			MaxContentScanFileSize: 20971520,  // 20MB
		},
		Tests: map[string]*config.TestConfig{
			"IsFreeOfKeywords": {
				Whitelist: []string{},
				Blacklist: []string{},
				KeywordArguments: []map[string]interface{}{
					{"keywords": []string{"password", "secret"}, "info": "Test keywords detected"},
				},
			},
		},
	}

	// Create mock archive files with the same base name but different extensions
	files := []structs.File{
		{Path: "test1.7z", Name: "test1.7z", IsArchive: true},
		{Path: "test1.tar", Name: "test1.tar", IsArchive: true},
		{Path: "test1.zip", Name: "test1.zip", IsArchive: true},
		{Path: "test2.7z", Name: "test2.7z", IsArchive: true},
		{Path: "test2.tar", Name: "test2.tar", IsArchive: true},
		{Path: "test2.zip", Name: "test2.zip", IsArchive: true},
		{Path: "regular.txt", Name: "regular.txt", IsArchive: false}, // Non-archive file
	}

	// Mock check function that always returns a message for archive files
	mockArchiveCheck := func(file structs.File, config config.Config) []structs.Message {
		if !file.IsArchive {
			return []structs.Message{}
		}
		return []structs.Message{
			{
				Content: "Test issue found in archive " + file.Name,
				Source:  file,
			},
		}
	}

	// Test the function that specifically handles archives
	messages := ApplyChecksFilteredByFileOnArchive(cfg, []func(structs.File, config.Config) []structs.Message{mockArchiveCheck}, files)

	// Verify that all archive files were processed
	expectedArchiveCount := 6 // 6 archive files
	if len(messages) != expectedArchiveCount {
		t.Fatalf("Expected %d messages (one per archive), got %d", expectedArchiveCount, len(messages))
	}

	// Verify each archive file generated a message
	archiveNames := make(map[string]bool)
	for _, msg := range messages {
		archiveNames[msg.Source.(structs.File).Name] = true
	}

	expectedArchives := []string{"test1.7z", "test1.tar", "test1.zip", "test2.7z", "test2.tar", "test2.zip"}
	for _, expected := range expectedArchives {
		if !archiveNames[expected] {
			t.Errorf("Missing message for archive: %s", expected)
		}
	}

	// Verify no duplicate processing
	if len(archiveNames) != expectedArchiveCount {
		t.Errorf("Expected %d unique archives, found %d", expectedArchiveCount, len(archiveNames))
	}
}
