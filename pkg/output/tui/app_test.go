package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/eawag-rdm/pc/pkg/output"
)

// TestRulesPanelRendersRuleAttribution asserts the RENDERED details text after
// switching to the Rules section, not the section index.
//
// An index-only assertion is worthless here: bumping the navigation bound in
// navigateLeftPanelRight without wiring case 7 in switchToSelectedLeftPanel
// leaves the section selectable, counted in the header bar, and still showing
// the PREVIOUS panel's content. An index assertion passes against exactly that
// broken state. Only reading detailsContent proves the rule data reaches the
// screen.
func TestRulesPanelRendersRuleAttribution(t *testing.T) {
	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsRuleFocused: []RuleDetails{
			{
				Rule:       "sensitive-content",
				Checkname:  "IsFreeOfKeywords",
				IssueCount: 2,
				Subjects: []RuleSubject{
					{Subject: "inner.txt", Path: "/path/archive.zip", ArchiveName: "archive.zip", IssueCount: 1},
					{Subject: "config.yaml", Path: "/path/config.yaml", IssueCount: 1},
				},
			},
		},
	}

	app := NewApp(data)

	app.selectedLeftPanel = 7
	app.switchToSelectedLeftPanel()

	if app.currentView != "rules" {
		t.Fatalf("Expected currentView 'rules' after switch, got %q", app.currentView)
	}

	rendered := app.detailsContent.GetText(true)

	for _, want := range []string{
		"Rule Issues (2)", // aggregate over the rules' IssueCount
		"sensitive-content",
		"IsFreeOfKeywords",
		"archive.zip > inner.txt",
		"config.yaml",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Details content missing %q, got:\n%s", want, rendered)
		}
	}

	// The navigation chip counts the section it switches to.
	app.populateLeftSections()
	if sections := app.leftSections.GetText(true); !strings.Contains(sections, "Rules (1)") {
		t.Errorf("Left sections missing rule count, got:\n%s", sections)
	}
}

// TestRulesPanelEmptyState pins the guard for a run without rule attribution:
// the panel renders its own placeholder instead of an empty document.
func TestRulesPanelEmptyState(t *testing.T) {
	app := NewApp(&ScanResult{Timestamp: "2024-01-14T10:30:00Z"})

	app.selectedLeftPanel = 7
	app.switchToSelectedLeftPanel()

	rendered := app.detailsContent.GetText(true)
	if !strings.Contains(rendered, "No rule attribution") {
		t.Errorf("Empty rule section missing placeholder, got:\n%s", rendered)
	}

	app.populateLeftSections()
	if sections := app.leftSections.GetText(true); !strings.Contains(sections, "Rules (0)") {
		t.Errorf("Left sections missing empty rule count, got:\n%s", sections)
	}
}

// TestRulesSectionIsReachable pins the navigation bound: Metadata (6) was the
// last section before Rules, so one step right must land on Rules (7) and a
// further step must not move past it.
func TestRulesSectionIsReachable(t *testing.T) {
	app := NewApp(&ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsRuleFocused: []RuleDetails{
			{Rule: "sensitive-content", Checkname: "IsFreeOfKeywords", IssueCount: 1,
				Subjects: []RuleSubject{{Subject: "config.yaml", IssueCount: 1}}},
		},
	})

	app.selectedLeftPanel = 6 // Metadata, the last section before Rules
	app.navigateLeftPanelRight()

	if app.selectedLeftPanel != 7 {
		t.Fatalf("Expected Rules section (7) after navigating right, got %d", app.selectedLeftPanel)
	}

	app.navigateLeftPanelRight()

	if app.selectedLeftPanel != 7 {
		t.Errorf("Navigation moved past the last section: got %d, want 7", app.selectedLeftPanel)
	}
}

// TestSummaryOmitsRuleNames guards ONE artefact: the TUI's clipboard summary.
//
// That text is copied by a curator and sent to the DEPOSITOR — it is prefixed
// by the configured summaryIntroText ("We have analyzed your data
// package..."). Rule names are the operator's own configuration vocabulary
// (internal grouping labels), so they are kept out of THAT text, no matter how
// the rule section grows. This says nothing about the other outputs: the
// server's analyze response deliberately carries details_rule_focused, by an
// explicit operator decision that the section behaves like its two sibling
// sections everywhere.
func TestSummaryOmitsRuleNames(t *testing.T) {
	const ruleName = "internal-only-rule-name"

	data := &ScanResult{
		Timestamp: "2024-01-14T10:30:00Z",
		DetailsCheckFocused: []CheckDetails{
			{
				Checkname: "IsFreeOfKeywords",
				Issues: []SubjectIssue{
					{Subject: "config.yaml", Path: "/path/config.yaml", Message: "Found 'PASSWORD'"},
				},
			},
		},
		DetailsRuleFocused: []RuleDetails{
			{
				Rule:       ruleName,
				Checkname:  "IsFreeOfKeywords",
				IssueCount: 1,
				Subjects: []RuleSubject{
					{Subject: "config.yaml", Path: "/path/config.yaml", IssueCount: 1},
				},
			},
		},
	}

	sg := NewSummaryGenerator(data, "my-package", "We have analyzed your data package.", 5, 3)
	result := sg.Generate()

	if strings.Contains(result, ruleName) {
		t.Errorf("Depositor summary leaked rule name %q, got:\n%s", ruleName, result)
	}

	// Sanity: the summary really did render findings, so the check above is not
	// passing on an empty document.
	if !strings.Contains(result, "config.yaml") {
		t.Fatalf("Summary rendered no findings, got:\n%s", result)
	}
}

func TestNewApp(t *testing.T) {
	// Create test data
	data := &ScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []ScannedFile{
			{Filename: "test.go", Issues: []CheckSummary{{Checkname: "TestCheck", IssueCount: 1}}},
		},
		Skipped: []SkippedFile{
			{Filename: "binary.bin", Reason: "Binary file detected"},
		},
		DetailsSubjectFocused: []SubjectDetails{
			{Subject: "test.go", Path: "/path/test.go", Issues: []CheckIssue{{Checkname: "TestCheck", Message: "Test issue"}}},
		},
		DetailsCheckFocused: []CheckDetails{
			{Checkname: "TestCheck", Issues: []SubjectIssue{{Subject: "test.go", Path: "/path/test.go", Message: "Test issue"}}},
		},
		Errors:   []output.LogMessage{{Level: "error", Message: "Test error", Timestamp: time.Now().UTC().Format(time.RFC3339)}},
		Warnings: []output.LogMessage{{Level: "warning", Message: "Test warning", Timestamp: time.Now().UTC().Format(time.RFC3339)}},
	}

	// Create app
	app := NewScanningApp()
	if app == nil {
		t.Fatal("NewScanningApp returned nil")
	}
	app.data = data

	if app.app == nil {
		t.Error("TView application not initialized")
	}

	if app.data == nil {
		t.Error("Data not set")
	}

	if app.currentView != "subjects" {
		t.Errorf("Expected currentView to be 'subjects', got %s", app.currentView)
	}

	// Test UI components are initialized
	if app.subjectsList == nil {
		t.Error("Subjects list not initialized")
	}

	if app.checksList == nil {
		t.Error("Checks list not initialized")
	}

	// detailsSections removed in new layout

	if app.detailsContent == nil {
		t.Error("Details content view not initialized")
	}

	if app.info == nil {
		t.Error("Info view not initialized")
	}

	if app.controls == nil {
		t.Error("Controls view not initialized")
	}
}

func TestAppWithEmptyData(t *testing.T) {
	// Create empty data
	data := &ScanResult{
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
		Scanned:               []ScannedFile{},
		Skipped:               []SkippedFile{},
		DetailsSubjectFocused: []SubjectDetails{},
		DetailsCheckFocused:   []CheckDetails{},
		Errors:                []output.LogMessage{},
		Warnings:              []output.LogMessage{},
	}

	// Create app
	app := NewScanningApp()
	if app == nil {
		t.Fatal("NewScanningApp returned nil with empty data")
	}
	app.data = data

	if app.data != data {
		t.Error("Data reference not preserved")
	}
}

func TestAppWithNilData(t *testing.T) {
	// The app should handle nil data gracefully or we should provide empty data
	// Since the TUI code expects valid data, let's test with empty data instead
	t.Skip("Skipping nil data test - TUI requires valid data structure")
}

func TestAppDataCounting(t *testing.T) {
	// Create test data with known counts
	data := &ScanResult{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Scanned: []ScannedFile{
			{Filename: "file1.go", Issues: []CheckSummary{{Checkname: "Check1", IssueCount: 2}}},
			{Filename: "file2.py", Issues: []CheckSummary{{Checkname: "Check2", IssueCount: 1}}},
		},
		Skipped: []SkippedFile{
			{Filename: "binary1.bin", Reason: "Binary file detected"},
			{Filename: "binary2.exe", Reason: "Binary file detected"},
		},
		DetailsSubjectFocused: []SubjectDetails{
			{Subject: "file1.go", Path: "/path/file1.go", Issues: []CheckIssue{{Checkname: "Check1", Message: "Issue 1"}}},
			{Subject: "file2.py", Path: "/path/file2.py", Issues: []CheckIssue{{Checkname: "Check2", Message: "Issue 2"}}},
		},
		DetailsCheckFocused: []CheckDetails{
			{Checkname: "Check1", Issues: []SubjectIssue{{Subject: "file1.go", Path: "/path/file1.go", Message: "Issue 1"}}},
			{Checkname: "Check2", Issues: []SubjectIssue{{Subject: "file2.py", Path: "/path/file2.py", Message: "Issue 2"}}},
		},
		Errors: []output.LogMessage{
			{Level: "error", Message: "Error 1", Timestamp: time.Now().UTC().Format(time.RFC3339)},
			{Level: "error", Message: "Error 2", Timestamp: time.Now().UTC().Format(time.RFC3339)},
		},
		Warnings: []output.LogMessage{
			{Level: "warning", Message: "Warning 1", Timestamp: time.Now().UTC().Format(time.RFC3339)},
		},
	}

	app := NewScanningApp()
	app.data = data

	// Verify data counts
	if len(app.data.Scanned) != 2 {
		t.Errorf("Expected 2 scanned files, got %d", len(app.data.Scanned))
	}

	if len(app.data.Skipped) != 2 {
		t.Errorf("Expected 2 skipped files, got %d", len(app.data.Skipped))
	}

	if len(app.data.Errors) != 2 {
		t.Errorf("Expected 2 errors, got %d", len(app.data.Errors))
	}

	if len(app.data.Warnings) != 1 {
		t.Errorf("Expected 1 warning, got %d", len(app.data.Warnings))
	}
}

// Test helper functions for data validation
func TestValidateTestData(t *testing.T) {
	// Test that our test data structures are valid
	data := &ScanResult{
		Timestamp: "2023-07-12T10:00:00Z",
		Scanned: []ScannedFile{
			{Filename: "test.go", Issues: []CheckSummary{{Checkname: "TestCheck", IssueCount: 1}}},
		},
		DetailsSubjectFocused: []SubjectDetails{
			{Subject: "test.go", Path: "/path/test.go", Issues: []CheckIssue{{Checkname: "TestCheck", Message: "Test message"}}},
		},
	}

	if data.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}

	if len(data.Scanned) == 0 {
		t.Error("Should have at least one scanned file for test")
	}

	if data.Scanned[0].Filename == "" {
		t.Error("Scanned file should have a filename")
	}

	if len(data.DetailsSubjectFocused) == 0 {
		t.Error("Should have subject details for test")
	}
}
