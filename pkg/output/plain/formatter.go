package plain

import (
	"fmt"
	"sort"
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

// FormatResults formats scan results as a concise plain text summary.
// Diagnostics are rendered as a trailing section, one line each: this is
// the terse surface, so a diagnostic gets its level, its subject when it
// has one, and its message - nothing more.
func (f *PlainFormatter) FormatResults(location string, messages []structs.Message, totalFiles int, diagnostics []structs.Diagnostic) string {
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
		writeDiagnosticsSection(&output, diagnostics)
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

	// Rule breakdown: which CONFIGURED rules reported, as opposed to which check.
	// A finding several rules produced counts under each. A finding with no rule
	// of its own is not counted - no rule name explains it. That covers metadata
	// findings and the findings of synthesized default rules, neither of which is
	// ever tagged; unlike the JSON section, this loop does not filter by source,
	// so it relies on that tagging invariant.
	ruleCounts := make(map[string]int)
	for _, msg := range issueMessages {
		for _, rule := range msg.Rules {
			ruleCounts[rule]++
		}
	}

	if len(ruleCounts) > 0 {
		// Sorted, unlike the map-ordered block above: this section is compared
		// in tests and read by humans across runs.
		ruleNames := make([]string, 0, len(ruleCounts))
		for ruleName := range ruleCounts {
			ruleNames = append(ruleNames, ruleName)
		}
		sort.Strings(ruleNames)

		output.WriteString("\nRules:\n")
		for _, ruleName := range ruleNames {
			output.WriteString(fmt.Sprintf("  • %s: %d\n", ruleName, ruleCounts[ruleName]))
		}
	}

	writeSkippedSection(&output, skippedFiles)
	writeDiagnosticsSection(&output, diagnostics)

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
		var name string
		switch source := msg.Source.(type) {
		case structs.File:
			name = source.QualifiedName()
		case structs.Repository:
			name = "repository"
		}
		if msg.TestName != "" {
			name += " [" + msg.TestName + "]"
		}
		reason := msg.Reason
		if reason == "" {
			reason = msg.Content
		}
		output.WriteString(fmt.Sprintf("  • %s: %s\n", name, reason))
	}
}

// writeDiagnosticsSection renders the run's operator-facing diagnostics.
// Errors come before warnings so the worst news reads first. Info is
// dropped deliberately, matching the JSON formatter's error/warning split
// (pkg/output/json/formatter.go), and so is any unrecognised level: an
// info-only run renders no section at all, not an empty heading.
func writeDiagnosticsSection(output *strings.Builder, diagnostics []structs.Diagnostic) {
	rendered := false
	for _, level := range []structs.DiagLevel{structs.DiagError, structs.DiagWarning} {
		prefix := "  ERROR   "
		if level == structs.DiagWarning {
			prefix = "  WARNING "
		}
		for _, d := range diagnostics {
			if d.Level != level {
				continue
			}
			if !rendered {
				output.WriteString("\n=== Diagnostics ===\n")
				rendered = true
			}
			if d.Subject != "" {
				fmt.Fprintf(output, "%s%s: %s\n", prefix, d.Subject, d.Message)
				continue
			}
			fmt.Fprintf(output, "%s%s\n", prefix, d.Message)
		}
	}
}
