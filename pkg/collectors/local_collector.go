package collectors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// validatePath ensures the path is safe and doesn't contain directory traversal patterns
func validatePath(path string) error {
	// Clean the path to resolve any ".." or "." components
	cleanPath := filepath.Clean(path)
	
	// Check for directory traversal patterns
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("path contains directory traversal patterns: %s", path)
	}
	
	// Check for absolute paths outside of reasonable bounds (security consideration)
	if filepath.IsAbs(cleanPath) {
		// Allow absolute paths but warn about potential risks
		// In a production environment, you might want to restrict this further
		fmt.Printf("Warning: Using absolute path: %s\n", cleanPath)
	}
	
	return nil
}

// securePath safely joins path components and validates the result
func securePath(base, name string) (string, error) {
	// Clean both components
	cleanBase := filepath.Clean(base)
	cleanName := filepath.Clean(name)
	
	// Ensure the file name doesn't contain traversal patterns
	if strings.Contains(cleanName, "..") || strings.Contains(cleanName, "/") || strings.Contains(cleanName, "\\") {
		return "", fmt.Errorf("unsafe file name: %s", name)
	}
	
	// Join paths securely
	fullPath := filepath.Join(cleanBase, cleanName)
	
	// Ensure the resulting path is still within the base directory
	if !strings.HasPrefix(fullPath, cleanBase) {
		return "", fmt.Errorf("path escape detected: %s -> %s", name, fullPath)
	}
	
	return fullPath, nil
}

// read all files from a local directory
func LocalCollector(path string, config config.Config) ([]structs.File, error) {
	collectorName := "LocalCollector"

	// Validate the input path
	if err := validatePath(path); err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// Clean the path
	cleanPath := filepath.Clean(path)
	
	foundFiles := []structs.File{}
	files, err := os.ReadDir(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", cleanPath, err)
	}

	for _, file := range files {
		// Securely construct the full path
		fullPath, err := securePath(cleanPath, file.Name())
		if err != nil {
			fmt.Printf("Skipping unsafe file: %s (%v)\n", file.Name(), err)
			continue
		}

		if !file.IsDir() {
			info, err := file.Info()
			if err != nil {
				fmt.Printf("Warning: could not get info for file %s: %v\n", file.Name(), err)
				continue
			}
			foundFiles = append(foundFiles, structs.ToFile(fullPath, file.Name(), info.Size(), ""))
		} else {
			// Check if folders should be included
			if attrs, ok := config.Collectors[collectorName].Attrs["includeFolders"]; ok && attrs == "true" {
				foundFiles = append(foundFiles, structs.ToFile(fullPath, file.Name(), -1, ""))
			}
		}
	}

	return foundFiles, nil
}
