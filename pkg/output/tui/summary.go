package tui

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// maxIssuesBeforeTruncation is the number of issues to show before truncating
	maxIssuesBeforeTruncation = 5
	// minGroupSizeForTruncation is the minimum group size to trigger truncation
	minGroupSizeForTruncation = 3
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

// groupedIssue wraps an issue with computed grouping keys for pattern detection
type groupedIssue struct {
	issue      SubjectIssue
	fullPath   string // "archive -> path/file" or just "path/file"
	parentPath string // parent directory for path-based grouping
	messageKey string // normalized message for message-based grouping
	index      int    // original index for stable ordering
}

// issueGroup represents a group of similar issues that can be truncated together
type issueGroup struct {
	key         string         // compound key: "parentPath|messageKey"
	parentPath  string         // the common parent path
	messageKey  string         // the common message pattern
	issues      []groupedIssue // issues in this group
	displayName string         // human-readable description for truncation message
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

		// Count for header
		totalIssues += len(issues)
		for _, issue := range issues {
			filesWithIssues[issue.Subject] = struct{}{}
		}

		// Human-readable check name
		displayName := humanizeCheckName(checkName)
		sb.WriteString(fmt.Sprintf("### %s (%d issue", displayName, len(issues)))
		if len(issues) != 1 {
			sb.WriteString("s")
		}
		sb.WriteString(")\n")

		// Format issues with smart truncation
		sb.WriteString(formatIssuesWithTruncation(issues))
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

// formatIssuesWithTruncation formats issues with automatic pattern-based truncation
func formatIssuesWithTruncation(issues []SubjectIssue) string {
	if len(issues) <= maxIssuesBeforeTruncation {
		// No truncation needed, output all
		var sb strings.Builder
		for _, issue := range issues {
			item := parseIssueItem(issue)
			sb.WriteString(formatIssueItem(item))
		}
		return sb.String()
	}

	// Convert to grouped issues with computed keys
	grouped := make([]groupedIssue, len(issues))
	for i, issue := range issues {
		grouped[i] = computeGroupingKeys(issue, i)
	}

	// Detect patterns and create groups
	groups := detectAndGroupIssues(grouped)

	// Format output with truncation
	return formatGroupedOutput(groups)
}

// computeGroupingKeys extracts grouping keys from an issue
func computeGroupingKeys(issue SubjectIssue, index int) groupedIssue {
	gi := groupedIssue{
		issue: issue,
		index: index,
	}

	// Build full path
	if issue.ArchiveName != "" {
		gi.fullPath = issue.ArchiveName + " -> " + issue.Subject
	} else {
		gi.fullPath = issue.Subject
	}

	// Extract parent path
	gi.parentPath = extractParentPath(gi.fullPath)

	// Normalize message for grouping
	gi.messageKey = normalizeMessageKey(issue.Message)

	return gi
}

// extractParentPath returns the parent directory portion of a path
// "archive.zip -> folder/subfolder/file.txt" -> "archive.zip -> folder/subfolder"
// "folder/file.txt" -> "folder"
func extractParentPath(path string) string {
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		return ""
	}
	return path[:lastSlash]
}

// normalizeMessageKey extracts the error type without specific matched values
// "Security credentials detected 'password'" -> "Security credentials detected"
// "Hardcoded file paths detected 'Q:'" -> "Hardcoded file paths detected"
func normalizeMessageKey(msg string) string {
	// Strip quoted values at the end (single quotes)
	if idx := strings.Index(msg, " '"); idx != -1 {
		return strings.TrimSpace(msg[:idx])
	}
	// Strip quoted values (double quotes)
	if idx := strings.Index(msg, " \""); idx != -1 {
		return strings.TrimSpace(msg[:idx])
	}
	return msg
}

// detectAndGroupIssues analyzes issues and groups them by compound key (parent path + message)
func detectAndGroupIssues(issues []groupedIssue) []issueGroup {
	// Group by compound key: parentPath + messageKey
	groupMap := make(map[string]*issueGroup)
	groupOrder := []string{} // Maintain insertion order

	for _, gi := range issues {
		// Create compound key
		compoundKey := gi.parentPath + "|" + gi.messageKey

		if group, exists := groupMap[compoundKey]; exists {
			group.issues = append(group.issues, gi)
		} else {
			groupMap[compoundKey] = &issueGroup{
				key:        compoundKey,
				parentPath: gi.parentPath,
				messageKey: gi.messageKey,
				issues:     []groupedIssue{gi},
			}
			groupOrder = append(groupOrder, compoundKey)
		}
	}

	// Convert to slice maintaining order
	result := make([]issueGroup, 0, len(groupOrder))
	for _, key := range groupOrder {
		group := groupMap[key]
		group.displayName = buildGroupDisplayName(group)
		result = append(result, *group)
	}

	return result
}

// buildGroupDisplayName creates a human-readable description for truncation message
func buildGroupDisplayName(group *issueGroup) string {
	var parts []string

	if group.parentPath != "" {
		parts = append(parts, fmt.Sprintf("\"%s\"", group.parentPath))
	}

	if group.messageKey != "" && group.parentPath != "" {
		parts = append(parts, fmt.Sprintf("with \"%s\"", group.messageKey))
	} else if group.messageKey != "" {
		parts = append(parts, fmt.Sprintf("\"%s\"", group.messageKey))
	}

	if len(parts) == 0 {
		return "similar issues"
	}

	return strings.Join(parts, " ")
}

// formatGroupedOutput formats groups with truncation where applicable
func formatGroupedOutput(groups []issueGroup) string {
	var sb strings.Builder

	for _, group := range groups {
		if len(group.issues) <= maxIssuesBeforeTruncation ||
			len(group.issues) < minGroupSizeForTruncation {
			// Small group - show all issues
			for _, gi := range group.issues {
				item := parseIssueItem(gi.issue)
				sb.WriteString(formatIssueItem(item))
			}
		} else {
			// Large group - show first N and truncate
			for i := 0; i < maxIssuesBeforeTruncation; i++ {
				item := parseIssueItem(group.issues[i].issue)
				sb.WriteString(formatIssueItem(item))
			}

			remaining := len(group.issues) - maxIssuesBeforeTruncation
			sb.WriteString(fmt.Sprintf("  ... and %d more in %s\n", remaining, group.displayName))
		}
	}

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
