# Task 07: Analyzer Bridge

**Status:** `[ ]` Not started

**Dependencies:** Task 02

## Objective

Create the bridge between the HTTP layer and the existing `pkg/*` analysis code.

## Deliverables

### 1. `internal/server/analyzer/analyzer.go`

```go
package analyzer

import (
    "path/filepath"

    "github.com/eawag-rdm/pc/internal/server/models"
    "github.com/eawag-rdm/pc/pkg/config"
    "github.com/eawag-rdm/pc/pkg/helpers"
    jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
    "github.com/eawag-rdm/pc/pkg/structs"
    "github.com/eawag-rdm/pc/pkg/utils"
)

// ProgressCallback is called during analysis to report progress
type ProgressCallback func(current, total int, message string)

// Analyzer bridges HTTP requests to the pkg/* analysis code
type Analyzer struct {
    config *config.Config
}

// New creates a new Analyzer with the given config file
func New(configPath string) (*Analyzer, error) {
    cfg, err := config.LoadConfig(configPath)
    if err != nil {
        return nil, err
    }
    return &Analyzer{config: cfg}, nil
}

// NewWithConfig creates an Analyzer with an existing config
func NewWithConfig(cfg *config.Config) *Analyzer {
    return &Analyzer{config: cfg}
}

// Analyze runs the analysis on the provided files
// Returns the JSON result string
func (a *Analyzer) Analyze(req *models.AnalysisRequest) (string, error)

// AnalyzeWithProgress runs analysis with progress reporting
func (a *Analyzer) AnalyzeWithProgress(req *models.AnalysisRequest, progress ProgressCallback) (string, error)

// convertFiles converts models.FileInfo to structs.File
func (a *Analyzer) convertFiles(files []models.FileInfo) ([]structs.File, error)

// Helper to detect if a file is an archive based on extension
func isArchive(path string) bool
```

### 2. Implementation Details

```go
func (a *Analyzer) AnalyzeWithProgress(req *models.AnalysisRequest, progress ProgressCallback) (string, error) {
    // Convert FileInfo to structs.File
    files, err := a.convertFiles(req.Files)
    if err != nil {
        return "", err
    }

    // Reset PDF tracker for this analysis
    helpers.PDFTracker.Reset()

    // Run analysis with progress callback
    var messages []structs.Message
    if progress != nil {
        messages = utils.ApplyAllChecksWithProgress(*a.config, files, true, progress)
    } else {
        messages = utils.ApplyAllChecks(*a.config, files, true)
    }

    // Format as JSON
    formatter := jsonformatter.NewJSONFormatter()
    result, err := formatter.FormatResults(
        req.CollectionID,
        "APICollector",
        messages,
        len(files),
        helpers.PDFTracker.Files,
    )
    if err != nil {
        return "", err
    }

    return result, nil
}

func (a *Analyzer) convertFiles(files []models.FileInfo) ([]structs.File, error) {
    result := make([]structs.File, len(files))

    for i, f := range files {
        // Get file info from filesystem
        info, err := os.Stat(f.Path)
        if err != nil {
            return nil, fmt.Errorf("cannot access file %s: %w", f.Path, err)
        }

        result[i] = structs.File{
            Path:      f.Path,
            Name:      filepath.Base(f.Path),
            Size:      info.Size(),
            Suffix:    filepath.Ext(f.Path),
            IsArchive: isArchive(f.Path),
        }
    }

    return result, nil
}
```

### 3. `internal/server/analyzer/analyzer_test.go`

```go
package analyzer

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/eawag-rdm/pc/internal/server/models"
)

func TestNew(t *testing.T) {
    // Test with valid config path
    // Test with invalid config path
}

func TestAnalyze(t *testing.T) {
    // Test basic analysis
    // Test with non-existent file (should error)
    // Test result is valid JSON
}

func TestAnalyzeWithProgress(t *testing.T) {
    // Test progress callback is called
    // Test progress reports correct values
}

func TestConvertFiles(t *testing.T) {
    // Test file conversion
    // Test archive detection
    // Test non-existent file error
}

func TestIsArchive(t *testing.T) {
    // Test .zip
    // Test .tar
    // Test .tar.gz
    // Test .7z
    // Test non-archive
}

// Helper to create test files
func createTestFile(t *testing.T, dir, name, content string) string {
    t.Helper()
    path := filepath.Join(dir, name)
    if err := os.WriteFile(path, []byte(content), 0644); err != nil {
        t.Fatalf("Failed to create test file: %v", err)
    }
    return path
}
```

## Tests Required

- `TestNew` - Analyzer creation
- `TestAnalyze` - Basic analysis flow
- `TestAnalyzeWithProgress` - Progress reporting
- `TestConvertFiles` - File conversion
- `TestIsArchive` - Archive detection

## Acceptance Criteria

- [ ] Analyzer can be created with config path
- [ ] Files are correctly converted to structs.File
- [ ] Analysis runs and produces valid JSON
- [ ] Progress callback is invoked correctly
- [ ] Errors are handled gracefully
- [ ] All tests pass
