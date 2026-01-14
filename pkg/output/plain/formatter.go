package plain

import (
	"fmt"
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// PlainFormatter provides plain text formatting for scan results
type PlainFormatter struct{}

// NewPlainFormatter creates a new plain text formatter
func NewPlainFormatter() *PlainFormatter {
	return &PlainFormatter{}
}

// FormatResults formats scan results as a concise plain text summary
func (f *PlainFormatter) FormatResults(location string, collectorName string, messages []structs.Message, totalFiles int, pdfFiles []string) string {
	var output strings.Builder
	
	// Header
	output.WriteString("=== PC Scan Results ===\n")
	output.WriteString(fmt.Sprintf("Location: %s\n", location))
	output.WriteString(fmt.Sprintf("Files scanned: %d\n", totalFiles))
	
	if len(messages) == 0 {
		output.WriteString("\n✅ No issues found!\n")
		return output.String()
	}
	
	// Group messages by source file (using display name with archive context)
	fileIssues := make(map[string][]structs.Message)
	repoIssues := []structs.Message{}

	for _, msg := range messages {
		switch source := msg.Source.(type) {
		case structs.File:
			// Create a key that includes archive context for proper grouping
			displayName := source.GetDisplayName()
			key := displayName
			if source.ArchiveName != "" {
				key = source.ArchiveName + " > " + displayName
			}
			fileIssues[key] = append(fileIssues[key], msg)
		case structs.Repository:
			repoIssues = append(repoIssues, msg)
		}
	}
	
	// Summary
	totalIssues := len(messages)
	filesWithIssues := len(fileIssues)
	if len(repoIssues) > 0 {
		filesWithIssues++ // Count repository as one more "file" with issues
	}
	
	output.WriteString(fmt.Sprintf("\n❌ Found %d issues in %d files:\n\n", totalIssues, filesWithIssues))
	
	// Repository issues first
	if len(repoIssues) > 0 {
		output.WriteString("📁 Repository Issues:\n")
		for _, msg := range repoIssues {
			output.WriteString(fmt.Sprintf("  • %s\n", msg.Content))
		}
		output.WriteString("\n")
	}
	
	// File issues grouped by file
	for filename, msgs := range fileIssues {
		output.WriteString(fmt.Sprintf("📄 %s (%d issues):\n", filename, len(msgs)))
		
		// Group by check type for better readability
		checkGroups := make(map[string][]structs.Message)
		for _, msg := range msgs {
			checkGroups[msg.TestName] = append(checkGroups[msg.TestName], msg)
		}
		
		for checkName, checkMsgs := range checkGroups {
			if len(checkMsgs) == 1 {
				output.WriteString(fmt.Sprintf("  • %s\n", checkMsgs[0].Content))
			} else {
				output.WriteString(fmt.Sprintf("  • %s (%d occurrences):\n", checkName, len(checkMsgs)))
				for _, msg := range checkMsgs {
					// Truncate long messages for readability
					content := msg.Content
					if len(content) > 80 {
						content = content[:77] + "..."
					}
					output.WriteString(fmt.Sprintf("    - %s\n", content))
				}
			}
		}
		output.WriteString("\n")
	}
	
	// Summary footer
	output.WriteString("=== Summary ===\n")
	output.WriteString(fmt.Sprintf("Total issues: %d\n", totalIssues))
	output.WriteString(fmt.Sprintf("Files with issues: %d/%d\n", filesWithIssues, totalFiles))
	
	// Issue type breakdown
	checkCounts := make(map[string]int)
	for _, msg := range messages {
		checkCounts[msg.TestName]++
	}
	
	if len(checkCounts) > 0 {
		output.WriteString("\nIssue types:\n")
		for checkName, count := range checkCounts {
			output.WriteString(fmt.Sprintf("  • %s: %d\n", checkName, count))
		}
	}
	
	return output.String()
}