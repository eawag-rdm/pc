package collectors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
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
		output.GlobalLogger.Warning("Warning: Using absolute path: %s", cleanPath)
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
	
	// Check if the path exists before attempting to walk it
	if _, err := os.Stat(cleanPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("path does not exist: %s", cleanPath)
		}
		return nil, fmt.Errorf("cannot access path %s: %w", cleanPath, err)
	}
	
	foundFiles := []structs.File{}
	
	// Check if folders should be included recursively
	includeFolders := false
	if attrs, ok := config.Collectors[collectorName].Attrs["includeFolders"]; ok {
		switch v := attrs.(type) {
		case bool:
			includeFolders = v
		case string:
			includeFolders = v == "true"
		}
	}
	
	// Use filepath.WalkDir for recursive traversal
	err := filepath.WalkDir(cleanPath, func(currentPath string, d os.DirEntry, err error) error {
		if err != nil {
			output.GlobalLogger.Warning("Warning: error accessing %s: %v", currentPath, err)
			return nil // Continue walking despite errors
		}
		
		// Skip the root directory itself
		if currentPath == cleanPath {
			return nil
		}
		
		if d.IsDir() {
			// Include directory only if includeFolders is true
			if includeFolders {
				foundFiles = append(foundFiles, structs.ToFile(currentPath, d.Name(), -1, ""))
			}
		} else {
			// Add regular files
			info, err := d.Info()
			if err != nil {
				output.GlobalLogger.Warning("Warning: could not get info for file %s: %v", currentPath, err)
				return nil
			}
			foundFiles = append(foundFiles, structs.ToFile(currentPath, d.Name(), info.Size(), ""))
		}
		
		return nil
	})
	
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", cleanPath, err)
	}

	return foundFiles, nil
}
