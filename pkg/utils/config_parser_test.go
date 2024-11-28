package utils

import (
	"os"
	"testing"
)

func TestParseTOML(t *testing.T) {
	// Create a temporary TOML file for testing
	tmpFile, err := os.CreateTemp("", "test.toml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write sample TOML content to the file
	content := `
		[checkToRun]
		whitelist = [".txt", ".md"]
		blacklist = [".exe", ".dll"]
		keywords = [{"arg1"= "value1"}, {"arg2"= "value2"}]
	`
	if _, err := tmpFile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	// Test parseTOML function
	config, err := parseTOML(tmpFile.Name())
	if err != nil {
		t.Fatalf("parseTOML failed: %v", err)
	}

	// Validate the parsed content
	checkToRun, ok := config["checkToRun"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected checkToRun section in config")
	}

	if whitelist, ok := checkToRun["whitelist"].([]interface{}); !ok || len(whitelist) != 2 {
		t.Errorf("Expected whitelist to have 2 elements, got %v", whitelist)
	} else {
		if whitelist[0] != ".txt" || whitelist[1] != ".md" {
			t.Errorf("Expected whitelist to be ['.txt', '.md'], got %v", whitelist)
		}
	}

	if blacklist, ok := checkToRun["blacklist"].([]interface{}); !ok || len(blacklist) != 2 {
		t.Errorf("Expected blacklist to have 2 elements, got %v", blacklist)
	} else {
		if blacklist[0] != ".exe" || blacklist[1] != ".dll" {
			t.Errorf("Expected blacklist to be ['.exe', '.dll'], got %v", blacklist)
		}
	}

	if keywords, ok := checkToRun["keywords"].([]interface{}); !ok || len(keywords) != 2 {
		t.Errorf("Expected keywords to have 2 elements, got %v", keywords)
	} else {
		keyword1, ok1 := keywords[0].(map[string]interface{})
		keyword2, ok2 := keywords[1].(map[string]interface{})
		if !ok1 || !ok2 {
			t.Errorf("Expected keywords to be a list of maps, got %v", keywords)
		} else {
			if keyword1["arg1"] != "value1" || keyword2["arg2"] != "value2" {
				t.Errorf("Expected keywords to be [{'arg1': 'value1'}, {'arg2': 'value2'}], got %v", keywords)
			}
		}
	}
}
func TestLoadConfig(t *testing.T) {
	// Create a temporary TOML file for testing
	tmpFile, err := os.CreateTemp("", "test.toml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write sample TOML content to the file
	content := `
		[tests.checkToRun]
		whitelist = [".txt", ".md"]
		blacklist = [".exe", ".dll"]
		keywords = {arg1 = "value1", arg2 = "value2"}
	`
	if _, err := tmpFile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Failed to close temp file: %v", err)
	}

	// Test LoadConfig function
	config, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Validate the parsed content
	checkToRun, ok := config.Tests["checkToRun"]
	if !ok {
		t.Fatalf("Expected checkToRun section in config")
	}

	if len(checkToRun.Whitelist) != 2 {
		t.Errorf("Expected whitelist to have 2 elements, got %v", checkToRun.Whitelist)
	} else {
		if checkToRun.Whitelist[0] != ".txt" || checkToRun.Whitelist[1] != ".md" {
			t.Errorf("Expected whitelist to be ['.txt', '.md'], got %v", checkToRun.Whitelist)
		}
	}

	if len(checkToRun.Blacklist) != 2 {
		t.Errorf("Expected blacklist to have 2 elements, got %v", checkToRun.Blacklist)
	} else {
		if checkToRun.Blacklist[0] != ".exe" || checkToRun.Blacklist[1] != ".dll" {
			t.Errorf("Expected blacklist to be ['.exe', '.dll'], got %v", checkToRun.Blacklist)
		}
	}

	if len(checkToRun.Keywords) != 2 {
		t.Errorf("Expected keywords to have 2 elements, got %v", checkToRun.Keywords)
	} else {
		if checkToRun.Keywords["arg1"] != "value1" || checkToRun.Keywords["arg2"] != "value2" {
			t.Errorf("Expected keywords to be {'arg1': 'value1', 'arg2': 'value2'}, got %v", checkToRun.Keywords)
		}
	}
}
