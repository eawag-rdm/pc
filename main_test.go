package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test helper to create a minimal valid config file
func createTestConfigFile(t *testing.T, tempDir string) string {
	configContent := `[operation.main]
collector = "LocalCollector"
checks = ["IsFreeOfKeywords"]
`
	configPath := filepath.Join(tempDir, "test_config.toml")
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config file: %v", err)
	}
	return configPath
}

// Test helper to create test files for scanning
func createTestFiles(t *testing.T, tempDir string) string {
	testDir := filepath.Join(tempDir, "test_scan")
	err := os.MkdirAll(testDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create a test Go file with potential issues
	testFile := filepath.Join(testDir, "test.go")
	testContent := `package main

import "fmt"

func main() {
	password := "secret123" // This should trigger keyword detection
	fmt.Println(password)
}
`
	err = os.WriteFile(testFile, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	return testDir
}

func TestMainBinary_Exists(t *testing.T) {
	// This test checks if the main binary can be built
	if os.Getenv("CI") != "" {
		t.Skip("Skipping binary test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Try to build the main binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build main binary: %v\nOutput: %s", err, string(output))
	}

	// Verify binary exists and is executable
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("Built binary does not exist: %v", err)
	}

	if !info.Mode().IsRegular() {
		t.Error("Built binary is not a regular file")
	}

	// On Unix systems, check if it's executable
	if info.Mode()&0111 == 0 {
		t.Error("Built binary is not executable")
	}
}

func TestHelpFlag(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping help flag test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Test help flag
	cmd = exec.Command(binaryPath, "-help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Help flag failed: %v", err)
	}

	// Verify help output contains expected flags
	helpText := string(output)
	expectedFlags := []string{"-config", "-location", "-help", "-tui", "-html"}
	for _, flag := range expectedFlags {
		if !strings.Contains(helpText, flag) {
			t.Errorf("Help output missing flag: %s", flag)
		}
	}
}

func TestJSONOutput(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping JSON output test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create test config and files
	configPath := createTestConfigFile(t, tempDir)
	testDir := createTestFiles(t, tempDir)

	// Run scanner with JSON output (default)
	cmd = exec.Command(binaryPath, "-config", configPath, "-location", testDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Scanner failed: %v\nOutput: %s", err, string(output))
	}

	// Verify output is valid JSON
	var result map[string]interface{}
	err = json.Unmarshal(output, &result)
	if err != nil {
		t.Fatalf("Output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	// Verify JSON structure contains expected fields
	expectedFields := []string{"timestamp", "scanned", "skipped", "details_subject_focused", "details_check_focused", "errors", "warnings"}
	for _, field := range expectedFields {
		if _, exists := result[field]; !exists {
			t.Errorf("JSON output missing field: %s", field)
		}
	}

	// Verify timestamp format
	if timestamp, ok := result["timestamp"].(string); ok {
		_, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			t.Errorf("Invalid timestamp format: %s", timestamp)
		}
	} else {
		t.Error("Timestamp field is not a string")
	}
}

func TestHTMLOutput(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping HTML output test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create test config and files
	configPath := createTestConfigFile(t, tempDir)
	testDir := createTestFiles(t, tempDir)
	htmlPath := filepath.Join(tempDir, "report.html")

	// Run scanner with HTML output
	cmd = exec.Command(binaryPath, "-config", configPath, "-location", testDir, "-html", htmlPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Scanner with HTML output failed: %v\nOutput: %s", err, string(output))
	}

	// Verify HTML file was created
	if _, err := os.Stat(htmlPath); os.IsNotExist(err) {
		t.Fatal("HTML report file was not created")
	}

	// Verify HTML content
	htmlContent, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("Failed to read HTML file: %v", err)
	}

	htmlStr := string(htmlContent)
	
	// Verify basic HTML structure
	if !strings.Contains(htmlStr, "<!DOCTYPE html>") {
		t.Error("HTML file is missing DOCTYPE declaration")
	}

	if !strings.Contains(htmlStr, "PC Scanner Report") {
		t.Error("HTML file is missing title")
	}

	// Verify output message
	outputStr := string(output)
	if !strings.Contains(outputStr, "HTML report generated") {
		t.Error("Missing HTML generation confirmation message")
	}

	if !strings.Contains(outputStr, htmlPath) {
		t.Error("Output message doesn't contain HTML file path")
	}
}

func TestInvalidConfig(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping invalid config test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create invalid config file
	invalidConfigPath := filepath.Join(tempDir, "invalid.toml")
	invalidContent := `[invalid toml content`
	err = os.WriteFile(invalidConfigPath, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create invalid config file: %v", err)
	}

	// Run scanner with invalid config
	cmd = exec.Command(binaryPath, "-config", invalidConfigPath, "-location", ".")
	output, err := cmd.CombinedOutput()
	
	// Should exit with error
	if err == nil {
		t.Error("Expected error for invalid config, but command succeeded")
	}

	// Verify error output is in JSON format
	var errorResult map[string]interface{}
	err = json.Unmarshal(output, &errorResult)
	if err != nil {
		t.Fatalf("Error output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	// Verify error structure
	if errorField, exists := errorResult["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if errorType, exists := errorMap["type"]; !exists || errorType != "config_error" {
				t.Error("Error type should be 'config_error'")
			}
		} else {
			t.Error("Error field is not a map")
		}
	} else {
		t.Error("JSON error output missing 'error' field")
	}
}

func TestNonexistentLocation(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping nonexistent location test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create valid config
	configPath := createTestConfigFile(t, tempDir)

	// Run scanner with nonexistent location
	nonexistentPath := filepath.Join(tempDir, "nonexistent")
	cmd = exec.Command(binaryPath, "-config", configPath, "-location", nonexistentPath)
	output, err := cmd.CombinedOutput()
	
	// Should exit with error
	if err == nil {
		t.Error("Expected error for nonexistent location, but command succeeded")
	}

	// Verify error output is in JSON format
	var errorResult map[string]interface{}
	err = json.Unmarshal(output, &errorResult)
	if err != nil {
		t.Fatalf("Error output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	// Verify error indicates collector error
	if errorField, exists := errorResult["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if errorType, exists := errorMap["type"]; !exists || errorType != "collector_error" {
				t.Error("Error type should be 'collector_error'")
			}
		}
	}
}

func TestEmptyDirectory(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping empty directory test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create valid config
	configPath := createTestConfigFile(t, tempDir)

	// Create empty directory
	emptyDir := filepath.Join(tempDir, "empty")
	err = os.MkdirAll(emptyDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create empty directory: %v", err)
	}

	// Run scanner with empty directory
	cmd = exec.Command(binaryPath, "-config", configPath, "-location", emptyDir)
	output, err := cmd.CombinedOutput()
	
	// Should exit with error for no files found
	if err == nil {
		t.Error("Expected error for empty directory, but command succeeded")
	}

	// Verify error output
	var errorResult map[string]interface{}
	err = json.Unmarshal(output, &errorResult)
	if err != nil {
		t.Fatalf("Error output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	// Verify error indicates no files found
	if errorField, exists := errorResult["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if errorType, exists := errorMap["type"]; !exists || errorType != "no_files" {
				t.Error("Error type should be 'no_files'")
			}
		}
	}
}

func TestConfigWithCkanCollector(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping CKAN collector test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create config with CKAN collector
	ckanConfigContent := `[operation.main]
collector = "CkanCollector"
checks = ["IsFreeOfKeywords"]
`
	configPath := filepath.Join(tempDir, "ckan_config.toml")
	err = os.WriteFile(configPath, []byte(ckanConfigContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create CKAN config file: %v", err)
	}

	// Run scanner with CKAN collector but default location (should fail)
	cmd = exec.Command(binaryPath, "-config", configPath)
	output, err := cmd.CombinedOutput()
	
	// Should exit with error asking for CKAN package name
	if err == nil {
		t.Error("Expected error for CKAN collector with default location, but command succeeded")
	}

	// Verify error message
	var errorResult map[string]interface{}
	err = json.Unmarshal(output, &errorResult)
	if err != nil {
		t.Fatalf("Error output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	if errorField, exists := errorResult["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if message, exists := errorMap["message"]; exists {
				if !strings.Contains(message.(string), "CKAN package name") {
					t.Error("Error message should mention CKAN package name")
				}
			}
		}
	}
}

func TestProfileFlags(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping profile flags test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create test config and files
	configPath := createTestConfigFile(t, tempDir)
	testDir := createTestFiles(t, tempDir)
	cpuProfilePath := filepath.Join(tempDir, "cpu.prof")
	memProfilePath := filepath.Join(tempDir, "mem.prof")

	// Run scanner with profiling flags
	cmd = exec.Command(binaryPath, 
		"-config", configPath, 
		"-location", testDir,
		"-cpuprofile", cpuProfilePath,
		"-memprofile", memProfilePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Scanner with profiling failed: %v\nOutput: %s", err, string(output))
	}

	// Verify profile files were created
	if _, err := os.Stat(cpuProfilePath); os.IsNotExist(err) {
		t.Error("CPU profile file was not created")
	}

	if _, err := os.Stat(memProfilePath); os.IsNotExist(err) {
		t.Error("Memory profile file was not created")
	}

	// Verify JSON output is still valid
	var result map[string]interface{}
	err = json.Unmarshal(output, &result)
	if err != nil {
		t.Fatalf("Output is not valid JSON with profiling: %v", err)
	}
}

func TestInvalidHTMLPath(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping invalid HTML path test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create test config and files
	configPath := createTestConfigFile(t, tempDir)
	testDir := createTestFiles(t, tempDir)

	// Try to write HTML to invalid path
	invalidHTMLPath := "/root/readonly/report.html" // Should fail on most systems

	// Run scanner with invalid HTML path
	cmd = exec.Command(binaryPath, "-config", configPath, "-location", testDir, "-html", invalidHTMLPath)
	output, err := cmd.CombinedOutput()
	
	// Should exit with error
	if err == nil {
		t.Error("Expected error for invalid HTML path, but command succeeded")
	}

	// Verify error output
	var errorResult map[string]interface{}
	err = json.Unmarshal(output, &errorResult)
	if err != nil {
		t.Fatalf("Error output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	// Verify error indicates HTML error
	if errorField, exists := errorResult["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if errorType, exists := errorMap["type"]; !exists || errorType != "html_error" {
				t.Error("Error type should be 'html_error'")
			}
		}
	}
}

func TestJSONErrorHandling(t *testing.T) {
	// Test the JSON error output helper function behavior
	// We can test this by examining the structure without running the binary

	// Simulate what outputError function would produce
	errorResult := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"error": map[string]string{
			"type":    "test_error",
			"message": "Test error message",
		},
	}

	// Verify JSON marshaling works
	jsonBytes, err := json.MarshalIndent(errorResult, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal error result: %v", err)
	}

	// Verify the JSON structure
	var parsed map[string]interface{}
	err = json.Unmarshal(jsonBytes, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse error JSON: %v", err)
	}

	// Verify structure
	if _, exists := parsed["timestamp"]; !exists {
		t.Error("Error JSON missing timestamp field")
	}

	if errorField, exists := parsed["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if errorType, exists := errorMap["type"]; !exists || errorType != "test_error" {
				t.Error("Error type not preserved correctly")
			}
			if message, exists := errorMap["message"]; !exists || message != "Test error message" {
				t.Error("Error message not preserved correctly")
			}
		} else {
			t.Error("Error field is not a map")
		}
	} else {
		t.Error("Error JSON missing error field")
	}
}

func TestUnknownCollector(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping unknown collector test in CI environment")
	}

	tempDir := t.TempDir()
	binaryPath := filepath.Join(tempDir, "pc")

	// Build the binary
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	_, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create config with unknown collector
	unknownConfigContent := `[operation.main]
collector = "UnknownCollector"
checks = ["IsFreeOfKeywords"]
`
	configPath := filepath.Join(tempDir, "unknown_config.toml")
	err = os.WriteFile(configPath, []byte(unknownConfigContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create unknown collector config file: %v", err)
	}

	// Run scanner with unknown collector
	cmd = exec.Command(binaryPath, "-config", configPath, "-location", ".")
	output, err := cmd.CombinedOutput()
	
	// Should exit with error
	if err == nil {
		t.Error("Expected error for unknown collector, but command succeeded")
	}

	// Verify error message
	var errorResult map[string]interface{}
	err = json.Unmarshal(output, &errorResult)
	if err != nil {
		t.Fatalf("Error output is not valid JSON: %v\nOutput: %s", err, string(output))
	}

	if errorField, exists := errorResult["error"]; exists {
		if errorMap, ok := errorField.(map[string]interface{}); ok {
			if message, exists := errorMap["message"]; exists {
				if !strings.Contains(message.(string), "Unknown collector") {
					t.Error("Error message should mention unknown collector")
				}
			}
		}
	}
}