package plain

import (
	"fmt"
	"strings"

	"github.com/eawag-rdm/pc/pkg/metadata"
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

	// Segregate skip acknowledgements from real issues. Skip Messages describe
	// files whose content was not scanned; per spec §6 they are surfaced in their
	// own section but never counted as issues (mirrors processMessages in
	// pkg/output/json/formatter.go).
	skippedFiles := []structs.Message{}
	issueMessages := make([]structs.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Skipped {
			skippedFiles = append(skippedFiles, msg)
			continue
		}
		issueMessages = append(issueMessages, msg)
	}

	if len(issueMessages) == 0 {
		output.WriteString("\n✅ No issues found!\n")
		writeSkippedSection(&output, skippedFiles)
		return output.String()
	}

	// Group messages by source file (using display name with archive context)
	fileIssues := make(map[string][]structs.Message)
	repoIssues := []structs.Message{}
	metaIssues := []structs.Message{}

	for _, msg := range issueMessages {
		switch source := msg.Source.(type) {
		case *metadata.Entity:
			metaIssues = append(metaIssues, msg)
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
	totalIssues := len(issueMessages)
	filesWithIssues := len(fileIssues)
	if len(repoIssues) > 0 {
		filesWithIssues++ // Count repository as one more "file" with issues
	}
	if len(metaIssues) > 0 {
		filesWithIssues++ // Count metadata as one more "subject" with issues
	}

	output.WriteString(fmt.Sprintf("\n❌ Found %d issues in %d files:\n\n", totalIssues, filesWithIssues))

	// Metadata issues first (metadata checks run before file checks)
	if len(metaIssues) > 0 {
		output.WriteString("🏷  Metadata Issues:\n")
		for _, msg := range metaIssues {
			output.WriteString(fmt.Sprintf("  • %s\n", msg.Content))
		}
		output.WriteString("\n")
	}

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
	for _, msg := range issueMessages {
		checkCounts[msg.TestName]++
	}

	if len(checkCounts) > 0 {
		output.WriteString("\nIssue types:\n")
		for checkName, count := range checkCounts {
			output.WriteString(fmt.Sprintf("  • %s: %d\n", checkName, count))
		}
	}

	writeSkippedSection(&output, skippedFiles)

	return output.String()
}

// writeSkippedSection renders skip acknowledgements as a non-issue "Skipped
// files" section. Skip Messages are never counted as issues (spec §6).
func writeSkippedSection(output *strings.Builder, skippedFiles []structs.Message) {
	if len(skippedFiles) == 0 {
		return
	}

	output.WriteString(fmt.Sprintf("\n⏭️  Skipped files (%d):\n", len(skippedFiles)))
	for _, msg := range skippedFiles {
		name := "repository"
		if source, ok := msg.Source.(structs.File); ok {
			name = source.GetDisplayName()
			if source.ArchiveName != "" {
				name = source.ArchiveName + " > " + name
			}
		}
		reason := msg.Reason
		if reason == "" {
			reason = msg.Content
		}
		output.WriteString(fmt.Sprintf("  • %s: %s\n", name, reason))
	}
}
