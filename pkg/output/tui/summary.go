package tui

import (
	"fmt"
	"sort"
	"strings"
)

// SummaryGenerator creates plain-text summaries grouped by check type
type SummaryGenerator struct {
	data     *ScanResult
	location string
}

// IssueItem represents a single issue for the summary
type IssueItem struct {
	Subject     string // filename or "Repository"
	ArchivePath string // inner path if from archive (empty if not from archive)
	Message     string // the issue content (without archive suffix)
}

// NewSummaryGenerator creates a generator from scan results
func NewSummaryGenerator(data *ScanResult, location string) *SummaryGenerator {
	return &SummaryGenerator{
		data:     data,
		location: location,
	}
}

// Generate creates the plain-text summary grouped by check type
func (sg *SummaryGenerator) Generate() string {
	if sg.data == nil {
		return "No scan data available."
	}

	var sb strings.Builder

	// Introductory text
	sb.WriteString("We have analyzed your data package and found a few issues. Please address them and get back to us once you're done. Then, we can continue with the publication process. Feel free to get back to us, if something is unclear.\n\n")

	// Header
	sb.WriteString("=== Package Checker Scan Summary ===\n")
	if sg.location != "" {
		sb.WriteString(fmt.Sprintf("Location: %s\n", sg.location))
	}
	if sg.data.Timestamp != "" {
		sb.WriteString(fmt.Sprintf("Timestamp: %s\n", sg.data.Timestamp))
	}
	sb.WriteString("\n")

	// Count total issues
	totalIssues := 0
	filesWithIssues := make(map[string]struct{})

	// Group issues by check type (already available in DetailsCheckFocused)
	if len(sg.data.DetailsCheckFocused) == 0 {
		sb.WriteString("No issues found.\n")
		return sb.String()
	}

	sb.WriteString("## Issues by Type\n\n")

	// Sort check names for consistent output
	checkNames := make([]string, 0, len(sg.data.DetailsCheckFocused))
	checkMap := make(map[string][]SubjectIssue)
	for _, check := range sg.data.DetailsCheckFocused {
		checkNames = append(checkNames, check.Checkname)
		checkMap[check.Checkname] = check.Issues
	}
	sort.Strings(checkNames)

	for _, checkName := range checkNames {
		issues := checkMap[checkName]
		if len(issues) == 0 {
			continue
		}

		// Human-readable check name
		displayName := humanizeCheckName(checkName)
		sb.WriteString(fmt.Sprintf("### %s (%d issue", displayName, len(issues)))
		if len(issues) != 1 {
			sb.WriteString("s")
		}
		sb.WriteString(")\n")

		for _, issue := range issues {
			totalIssues++
			filesWithIssues[issue.Subject] = struct{}{}

			item := parseIssueItem(issue)
			sb.WriteString(formatIssueItem(item))
		}
		sb.WriteString("\n")
	}

	// Summary footer
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("Total: %d issue", totalIssues))
	if totalIssues != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(fmt.Sprintf(" in %d file", len(filesWithIssues)))
	if len(filesWithIssues) != 1 {
		sb.WriteString("s")
	}
	sb.WriteString("\n")

	return sb.String()
}

// parseIssueItem extracts archive context from an issue
func parseIssueItem(issue SubjectIssue) IssueItem {
	item := IssueItem{
		Subject: issue.Subject,
		Message: issue.Message,
	}

	// Use structured ArchiveName field if present
	if issue.ArchiveName != "" {
		item.ArchivePath = issue.Subject // The subject is the file path within archive
		item.Subject = issue.ArchiveName // The archive name becomes the main subject
	}

	return item
}

// formatIssueItem formats a single issue for display
func formatIssueItem(item IssueItem) string {
	var sb strings.Builder
	sb.WriteString("  - ")

	if item.ArchivePath != "" {
		// Archive issue: "archive.zip -> inner/file.txt: message"
		sb.WriteString(item.Subject)
		sb.WriteString(" -> ")
		sb.WriteString(item.ArchivePath)
	} else if item.Subject == "Repository" || item.Subject == "" {
		// Repository-level issue
		sb.WriteString("Repository")
	} else {
		// Regular file issue
		sb.WriteString(item.Subject)
	}

	// Add message if present and different from subject
	if item.Message != "" && item.Message != item.Subject {
		sb.WriteString(": ")
		sb.WriteString(item.Message)
	}

	sb.WriteString("\n")
	return sb.String()
}

// humanizeCheckName converts internal check names to human-readable form
func humanizeCheckName(checkName string) string {
	// Map of known check names to human-readable versions
	nameMap := map[string]string{
		"IsFreeOfKeywords":              "Possible sensitive content detected",
		"HasValidFileName":              "File name issues",
		"HasValidNameLength":            "File name too long",
		"IsFreeOfSpecialChars":          "Special characters in file name",
		"IsFreeOfLeadingTrailingSpaces": "Leading or trailing spaces in file name",
		"HasOnlyASCIIChars":             "Non-ASCII characters in file name",
		"HasReadMe":                     "Missing README file",
		"HasValidTOCTree":               "Table of contents issues",
		"HasNoInvalidFileNames":         "Invalid file names in archive",
		"HasNoEmptyFolders":             "Empty folders in archive",
		"HasNoHiddenFiles":              "Hidden files in archive",
	}

	if humanName, ok := nameMap[checkName]; ok {
		return humanName
	}

	// Fallback: convert CamelCase to spaces
	return checkName
}
