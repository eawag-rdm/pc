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

// localCollectorAttrs holds the resolved [collector.LocalCollector] attrs.
// depth: 0 = top-level files only (default), N = descend N directory levels,
// -1 = unlimited. maxFileCount: stop the walk once this many entries are
// collected (0 = no cap).
type localCollectorAttrs struct {
	depth        int
	maxFileCount int64
}

// localAttrsFrom resolves the attrs. includeFolders (legacy bool, string form
// "true" accepted) without maxFolderDepth keeps its old meaning of unlimited
// recursion; an explicit maxFolderDepth always wins.
func localAttrsFrom(cfg config.Config) (localCollectorAttrs, error) {
	a := localCollectorAttrs{}
	cc, ok := cfg.Collectors["LocalCollector"]
	if !ok || cc == nil || cc.Attrs == nil {
		return a, nil
	}
	switch v := cc.Attrs["includeFolders"].(type) {
	case bool:
		if v {
			a.depth = -1
		}
	case string:
		if v == "true" {
			a.depth = -1
		}
	}
	if v, ok := cc.Attrs["maxFolderDepth"].(int64); ok {
		if v < 0 {
			return a, fmt.Errorf("[collector.LocalCollector] attrs.maxFolderDepth must be >= 0, got %d", v)
		}
		a.depth = int(v)
	}
	if v, ok := cc.Attrs["maxFileCount"].(int64); ok {
		if v < 0 {
			return a, fmt.Errorf("[collector.LocalCollector] attrs.maxFileCount must be >= 0, got %d", v)
		}
		a.maxFileCount = v
	}
	return a, nil
}

// read all files from a local directory
func LocalCollector(path string, config config.Config) ([]structs.File, error) {
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

	attrs, err := localAttrsFrom(config)
	if err != nil {
		return nil, err
	}

	foundFiles := []structs.File{}
	sep := string(os.PathSeparator)

	err = filepath.WalkDir(cleanPath, func(currentPath string, d os.DirEntry, err error) error {
		if err != nil {
			output.GlobalLogger.Warning("Warning: error accessing %s: %v", currentPath, err)
			return nil // Continue walking despite errors
		}

		// Skip the root directory itself
		if currentPath == cleanPath {
			return nil
		}

		// SkipAll (not a no-op continue) so the cap bounds worst-case I/O.
		if attrs.maxFileCount > 0 && int64(len(foundFiles)) >= attrs.maxFileCount {
			output.GlobalLogger.Warning("LocalCollector: maxFileCount (%d) reached; remaining entries under '%s' are not collected", attrs.maxFileCount, cleanPath)
			return filepath.SkipAll
		}

		if d.IsDir() {
			if attrs.depth == 0 {
				// Top-level-only mode: directories are neither listed nor entered.
				return filepath.SkipDir
			}
			foundFiles = append(foundFiles, structs.ToFile(currentPath, d.Name(), -1, ""))
			if attrs.depth > 0 {
				rel := strings.TrimPrefix(strings.TrimPrefix(currentPath, cleanPath), sep)
				if strings.Count(rel, sep) >= attrs.depth {
					// Boundary-depth directory: listed but not descended.
					return filepath.SkipDir
				}
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
