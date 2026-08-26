package json

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestNewJSONFormatter(t *testing.T) {
	formatter := NewJSONFormatter()

	if formatter == nil {
		t.Fatal("NewJSONFormatter returned nil")
	}
}

func TestFormatResults_EmptyMessages(t *testing.T) {
	formatter := NewJSONFormatter()
	messages := []structs.Message{}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 0, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Verify it's valid JSON
	var scanResult ScanResult
	err = json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// Verify basic structure
	if scanResult.Timestamp == "" {
		t.Error("Timestamp not set")
	}

	if scanResult.Scanned == nil {
		t.Error("Scanned slice is nil")
	} else if len(scanResult.Scanned) != 0 {
		t.Errorf("Expected empty scanned slice, got %d items", len(scanResult.Scanned))
	}

	if scanResult.Skipped == nil {
		t.Error("Skipped slice is nil")
	}
}

func TestFormatResults_NilPDFFiles(t *testing.T) {
	formatter := NewJSONFormatter()

	result, err := formatter.FormatResults("/test/location", "CkanCollector", []structs.Message{}, 0, nil, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// pdf_files must serialize as [] even when the caller passes nil (e.g. an
	// empty FileTracker.SnapshotFiles), never as JSON null.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &raw); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}
	if string(raw["pdf_files"]) == "null" {
		t.Error("pdf_files serialized as null; want []")
	}
}

func TestFormatResults_WithMessages(t *testing.T) {
	formatter := NewJSONFormatter()

	// Create test file
	testFile := structs.File{
		Name: "test.go",
		Path: "/path/to/test.go",
	}

	messages := []structs.Message{
		{
			Content:  "Found keyword 'password'",
			Source:   testFile,
			TestName: "IsFreeOfKeywords",
		},
		{
			Content:  "File contains secrets",
			Source:   testFile,
			TestName: "SecretDetection",
		},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 1, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Parse result
	var scanResult ScanResult
	err = json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// Verify scanned files
	if len(scanResult.Scanned) != 1 {
		t.Fatalf("Expected 1 scanned file, got %d", len(scanResult.Scanned))
	}

	scannedFile := scanResult.Scanned[0]
	if scannedFile.Filename != "test.go" {
		t.Errorf("Expected filename 'test.go', got '%s'", scannedFile.Filename)
	}

	if len(scannedFile.Issues) != 2 {
		t.Fatalf("Expected 2 issues, got %d", len(scannedFile.Issues))
	}

	// Verify subject-focused details
	if len(scanResult.DetailsSubjectFocused) != 1 {
		t.Fatalf("Expected 1 subject detail, got %d", len(scanResult.DetailsSubjectFocused))
	}

	subjectDetail := scanResult.DetailsSubjectFocused[0]
	if subjectDetail.Subject != "test.go" {
		t.Errorf("Expected subject 'test.go', got '%s'", subjectDetail.Subject)
	}

	if len(subjectDetail.Issues) != 2 {
		t.Fatalf("Expected 2 issues in subject detail, got %d", len(subjectDetail.Issues))
	}

	// Verify check-focused details
	if len(scanResult.DetailsCheckFocused) != 2 {
		t.Fatalf("Expected 2 check details, got %d", len(scanResult.DetailsCheckFocused))
	}
}

func TestFormatResults_RepositoryMessage(t *testing.T) {
	formatter := NewJSONFormatter()

	// Create repository message (not associated with a file)
	repo := structs.Repository{Files: []structs.File{}}
	messages := []structs.Message{
		{
			Content:  "Repository-level issue",
			Source:   repo, // Repository source, not string
			TestName: "RepositoryCheck",
		},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 0, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Parse result
	var scanResult ScanResult
	err = json.Unmarshal([]byte(result), &scanResult)
	if err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// Repository messages shouldn't create scanned files
	if len(scanResult.Scanned) != 0 {
		t.Errorf("Expected 0 scanned files for repository message, got %d", len(scanResult.Scanned))
	}

	// But should appear in subject-focused details as "repository"
	if len(scanResult.DetailsSubjectFocused) != 1 {
		t.Fatalf("Expected 1 subject detail, got %d", len(scanResult.DetailsSubjectFocused))
	}

	if scanResult.DetailsSubjectFocused[0].Subject != "repository" {
		t.Errorf("Expected subject 'repository', got '%s'", scanResult.DetailsSubjectFocused[0].Subject)
	}
}

func TestFormatResults_SkippedMessageRoutedToSkipped(t *testing.T) {
	formatter := NewJSONFormatter()

	oversizedFile := structs.File{
		Name:        "huge.bin",
		Path:        "/path/to/huge.bin",
		DisplayName: "huge.bin",
	}

	reason := "Skipped content scan of file: file size (2000 bytes) exceeds maximum (1000 bytes)."
	normalFile := structs.File{Name: "ok.txt", Path: "/path/to/ok.txt"}

	messages := []structs.Message{
		{
			Content:  reason,
			Source:   oversizedFile,
			TestName: "IsFreeOfKeywords",
			Skipped:  true,
			Reason:   reason,
		},
		{
			Content:  "Found keyword 'secret'",
			Source:   normalFile,
			TestName: "IsFreeOfKeywords",
		},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 2, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	var scanResult ScanResult
	if err := json.Unmarshal([]byte(result), &scanResult); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	// The skip message must land in skipped[].
	if len(scanResult.Skipped) != 1 {
		t.Fatalf("Expected 1 skipped file, got %d", len(scanResult.Skipped))
	}
	if scanResult.Skipped[0].Filename != "huge.bin" {
		t.Errorf("Expected skipped filename 'huge.bin', got '%s'", scanResult.Skipped[0].Filename)
	}
	if scanResult.Skipped[0].Path != "/path/to/huge.bin" {
		t.Errorf("Expected skipped path '/path/to/huge.bin', got '%s'", scanResult.Skipped[0].Path)
	}
	if scanResult.Skipped[0].Reason != reason {
		t.Errorf("Expected skipped reason '%s', got '%s'", reason, scanResult.Skipped[0].Reason)
	}

	// The skip message must NOT appear as a scanned file or as an issue.
	for _, scanned := range scanResult.Scanned {
		if scanned.Filename == "huge.bin" {
			t.Errorf("Skipped file 'huge.bin' must not appear in scanned[]")
		}
	}
	for _, detail := range scanResult.DetailsSubjectFocused {
		if detail.Subject == "huge.bin" {
			t.Errorf("Skipped file 'huge.bin' must not appear in details_subject_focused[]")
		}
	}

	// The non-skip message must still be processed as a real issue.
	if len(scanResult.Scanned) != 1 || scanResult.Scanned[0].Filename != "ok.txt" {
		t.Errorf("Expected only 'ok.txt' in scanned[], got %+v", scanResult.Scanned)
	}
}

func TestFormatResults_SkippedArchiveMemberFilenameIncludesArchive(t *testing.T) {
	formatter := NewJSONFormatter()

	member := structs.ToFileWithDisplay(
		"/path/to/archive.zip",
		"inner/big.txt",
		"inner/big.txt",
		5000,
		"",
		"archive.zip",
	)
	reason := "Skipped content scan of archive member: would exceed total archive memory limit (100 bytes)."
	messages := []structs.Message{
		{Content: reason, Source: member, TestName: "IsFreeOfKeywords", Skipped: true, Reason: reason},
	}

	result, err := formatter.FormatResults("/loc", "LocalCollector", messages, 1, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	var scanResult ScanResult
	if err := json.Unmarshal([]byte(result), &scanResult); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}

	if len(scanResult.Skipped) != 1 {
		t.Fatalf("Expected 1 skipped entry, got %d", len(scanResult.Skipped))
	}
	if scanResult.Skipped[0].Filename != "archive.zip > inner/big.txt" {
		t.Errorf("Expected archive-qualified filename, got '%s'", scanResult.Skipped[0].Filename)
	}
	if len(scanResult.Scanned) != 0 {
		t.Errorf("Skip-only input must not produce scanned[] entries, got %d", len(scanResult.Scanned))
	}
}

func TestProcessMessages(t *testing.T) {
	result := &ScanResult{}

	testFile := structs.File{
		Name: "example.txt",
		Path: "/path/to/example.txt",
	}

	messages := []structs.Message{
		{
			Content:  "Test message 1",
			Source:   testFile,
			TestName: "TestCheck1",
		},
		{
			Content:  "Test message 2",
			Source:   testFile,
			TestName: "TestCheck2",
		},
	}

	result.processMessages(messages)

	// Verify scanned files
	if len(result.Scanned) != 1 {
		t.Fatalf("Expected 1 scanned file, got %d", len(result.Scanned))
	}

	if result.Scanned[0].Filename != "example.txt" {
		t.Errorf("Expected filename 'example.txt', got '%s'", result.Scanned[0].Filename)
	}

	if len(result.Scanned[0].Issues) != 2 {
		t.Fatalf("Expected 2 issues, got %d", len(result.Scanned[0].Issues))
	}

	// Verify subject details
	if len(result.DetailsSubjectFocused) != 1 {
		t.Fatalf("Expected 1 subject detail, got %d", len(result.DetailsSubjectFocused))
	}

	if len(result.DetailsSubjectFocused[0].Issues) != 2 {
		t.Fatalf("Expected 2 issues in subject detail, got %d", len(result.DetailsSubjectFocused[0].Issues))
	}

	// Verify check details
	if len(result.DetailsCheckFocused) != 2 {
		t.Fatalf("Expected 2 check details, got %d", len(result.DetailsCheckFocused))
	}
}

func TestRuleFocusedGroupsByRule(t *testing.T) {
	result := &ScanResult{}

	testFile := structs.File{Name: "test.txt", Path: "/path/to/test.txt"}

	messages := []structs.Message{
		{Content: "Found keyword 'password'", Source: testFile, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}},
		{Content: "Found keyword 'todo'", Source: testFile, TestName: "IsFreeOfKeywords", Rules: []string{"drafts"}},
	}

	result.processMessages(messages)

	if len(result.DetailsRuleFocused) != 2 {
		t.Fatalf("Expected 2 rule details, got %d", len(result.DetailsRuleFocused))
	}

	for _, rule := range result.DetailsRuleFocused {
		if rule.Checkname != "IsFreeOfKeywords" {
			t.Errorf("Rule '%s': expected checkname 'IsFreeOfKeywords', got '%s'", rule.Rule, rule.Checkname)
		}
		if rule.IssueCount != 1 {
			t.Errorf("Rule '%s': expected issue_count 1, got %d", rule.Rule, rule.IssueCount)
		}
	}

	if result.DetailsRuleFocused[0].Rule != "drafts" || result.DetailsRuleFocused[1].Rule != "secrets" {
		t.Errorf("Expected rules [drafts secrets], got [%s %s]",
			result.DetailsRuleFocused[0].Rule, result.DetailsRuleFocused[1].Rule)
	}
}

func TestRuleFocusedKeyedBySubjectKey(t *testing.T) {
	result := &ScanResult{}

	memberA := structs.ToFileWithDisplay("/path/to/a.zip", "notes.txt", "notes.txt", 100, "", "a.zip")
	memberB := structs.ToFileWithDisplay("/path/to/b.zip", "notes.txt", "notes.txt", 100, "", "b.zip")

	messages := []structs.Message{
		{Content: "Found keyword 'password'", Source: memberA, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}},
		{Content: "Found keyword 'password'", Source: memberB, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}},
	}

	result.processMessages(messages)

	if len(result.DetailsRuleFocused) != 1 {
		t.Fatalf("Expected 1 rule detail, got %d", len(result.DetailsRuleFocused))
	}

	rule := result.DetailsRuleFocused[0]
	if rule.IssueCount != 2 {
		t.Errorf("Expected rule issue_count 2, got %d", rule.IssueCount)
	}

	// Same display name in two archives must stay two subjects.
	if len(rule.Subjects) != 2 {
		t.Fatalf("Expected 2 subjects, got %d: %+v", len(rule.Subjects), rule.Subjects)
	}
	for _, sub := range rule.Subjects {
		if sub.IssueCount != 1 {
			t.Errorf("Subject '%s > %s': expected issue_count 1, got %d", sub.ArchiveName, sub.Subject, sub.IssueCount)
		}
	}
	if rule.Subjects[0].ArchiveName != "a.zip" || rule.Subjects[1].ArchiveName != "b.zip" {
		t.Errorf("Expected archives [a.zip b.zip], got [%s %s]",
			rule.Subjects[0].ArchiveName, rule.Subjects[1].ArchiveName)
	}

	// Subject carries the display name, never the composite subject key.
	for _, sub := range rule.Subjects {
		if sub.Subject != "notes.txt" {
			t.Errorf("Expected subject display name 'notes.txt', got '%s'", sub.Subject)
		}
	}
	if rule.Subjects[0].Path != "/path/to/a.zip" || rule.Subjects[1].Path != "/path/to/b.zip" {
		t.Errorf("Expected paths [/path/to/a.zip /path/to/b.zip], got [%s %s]",
			rule.Subjects[0].Path, rule.Subjects[1].Path)
	}
}

func TestRuleFocusedExcludesRulelessMessages(t *testing.T) {
	result := &ScanResult{}

	plainFile := structs.File{Name: "plain.txt", Path: "/path/to/plain.txt"}
	skippedFile := structs.File{Name: "huge.bin", Path: "/path/to/huge.bin"}

	reason := "Skipped content scan of file: file size exceeds maximum."
	messages := []structs.Message{
		// Load-bearing: a finding with no rule of its own must not create a rule.
		{Content: "Filename contains a space", Source: plainFile, TestName: "HasNoWhitespace"},
		// Documents an inherited exclusion: skips are routed out earlier in the
		// message loop, so this section never sees them.
		{Content: reason, Source: skippedFile, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}, Skipped: true, Reason: reason},
	}

	result.processMessages(messages)

	if len(result.DetailsRuleFocused) != 0 {
		t.Fatalf("Expected 0 rule details, got %d: %+v", len(result.DetailsRuleFocused), result.DetailsRuleFocused)
	}
}

// TestRuleFocusedTotalEqualsSubjectCounts pins the exact half of the counting
// invariant: per rule, issue_count is the sum of that rule's subjects' counts.
// The reconciliation with the check is an inequality - the rules of one check
// count ATTRIBUTIONS, and only where no finding names two rules do they add up
// to the check's finding count.
func TestRuleFocusedTotalEqualsSubjectCounts(t *testing.T) {
	result := &ScanResult{}

	testFile := structs.File{Name: "test.txt", Path: "/path/to/test.txt"}

	messages := []structs.Message{
		{Content: "Found keyword 'password' (line 1)", Source: testFile, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}},
		{Content: "Found keyword 'password' (line 7)", Source: testFile, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}},
		{Content: "Found keyword 'password' (line 9)", Source: testFile, TestName: "IsFreeOfKeywords", Rules: []string{"secrets"}},
	}

	result.processMessages(messages)

	if len(result.DetailsRuleFocused) != 1 {
		t.Fatalf("Expected 1 rule detail, got %d", len(result.DetailsRuleFocused))
	}
	rule := result.DetailsRuleFocused[0]
	if rule.IssueCount != 3 {
		t.Errorf("Expected rule issue_count 3, got %d", rule.IssueCount)
	}
	if len(rule.Subjects) != 1 {
		t.Fatalf("Expected 1 subject, got %d", len(rule.Subjects))
	}
	if rule.Subjects[0].IssueCount != 3 {
		t.Errorf("Expected subject issue_count 3, got %d", rule.Subjects[0].IssueCount)
	}

	// The rule's count must reconcile with the same check's issues. The general
	// invariant is issue_count >= the check's findings; here it is an equality,
	// because no finding of this check names more than one rule.
	checkIssues := 0
	for _, check := range result.DetailsCheckFocused {
		if check.Checkname == rule.Checkname {
			checkIssues = len(check.Issues)
		}
	}
	if checkIssues != rule.IssueCount {
		t.Errorf("Rule issue_count %d does not reconcile with check '%s' issues %d",
			rule.IssueCount, rule.Checkname, checkIssues)
	}
}

// TestRuleFocusedCountsSharedFindingUnderEveryRule is the strict case of that
// inequality: ONE finding produced on behalf of two rules is counted under each
// of them, while details_check_focused - the authoritative finding count - still
// counts it once.
func TestRuleFocusedCountsSharedFindingUnderEveryRule(t *testing.T) {
	result := &ScanResult{}

	testFile := structs.File{Name: "test.txt", Path: "/path/to/test.txt"}

	messages := []structs.Message{
		{Content: "Found keyword 'password'", Source: testFile, TestName: "IsFreeOfKeywords", Rules: []string{"secrets", "drafts"}},
	}

	result.processMessages(messages)

	if len(result.DetailsRuleFocused) != 2 {
		t.Fatalf("Expected the finding under both rules, got %d: %+v", len(result.DetailsRuleFocused), result.DetailsRuleFocused)
	}
	if result.DetailsRuleFocused[0].Rule != "drafts" || result.DetailsRuleFocused[1].Rule != "secrets" {
		t.Errorf("Expected rules [drafts secrets], got [%s %s]",
			result.DetailsRuleFocused[0].Rule, result.DetailsRuleFocused[1].Rule)
	}

	for _, rule := range result.DetailsRuleFocused {
		if rule.IssueCount != 1 {
			t.Errorf("Rule '%s': expected issue_count 1, got %d", rule.Rule, rule.IssueCount)
		}
		if len(rule.Subjects) != 1 || rule.Subjects[0].IssueCount != 1 {
			t.Errorf("Rule '%s': expected one subject with issue_count 1, got %+v", rule.Rule, rule.Subjects)
		}
	}

	// The two rules sum to 2 attributions of ONE finding; the check section is
	// the count that must not follow them.
	checkIssues := 0
	for _, check := range result.DetailsCheckFocused {
		if check.Checkname == "IsFreeOfKeywords" {
			checkIssues = len(check.Issues)
		}
	}
	if checkIssues != 1 {
		t.Errorf("details_check_focused counted %d issues, want the one finding counted once", checkIssues)
	}
}

func TestRuleFocusedSerializesEmptyArray(t *testing.T) {
	formatter := NewJSONFormatter()

	testFile := structs.File{Name: "test.txt", Path: "/path/to/test.txt"}
	messages := []structs.Message{
		{Content: "Filename contains a space", Source: testFile, TestName: "HasNoWhitespace"},
	}

	result, err := formatter.FormatResults("/test/location", "LocalCollector", messages, 1, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	if !strings.Contains(result, `"details_rule_focused": []`) {
		t.Errorf("Expected empty details_rule_focused array in JSON, got:\n%s", result)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &raw); err != nil {
		t.Fatalf("Result is not valid JSON: %v", err)
	}
	if string(raw["details_rule_focused"]) == "null" {
		t.Error("details_rule_focused serialized as null; want []")
	}
}

func TestRuleFocusedOrderStable(t *testing.T) {
	result := &ScanResult{}

	zebra := structs.File{Name: "zebra.txt", Path: "/path/to/zebra.txt"}
	alpha := structs.File{Name: "alpha.txt", Path: "/path/to/alpha.txt"}

	messages := []structs.Message{
		{Content: "m1", Source: zebra, TestName: "IsFreeOfKeywords", Rules: []string{"zulu"}},
		{Content: "m2", Source: alpha, TestName: "IsFreeOfKeywords", Rules: []string{"mike"}},
		{Content: "m3", Source: zebra, TestName: "IsFreeOfKeywords", Rules: []string{"alfa"}},
		{Content: "m4", Source: alpha, TestName: "IsFreeOfKeywords", Rules: []string{"alfa"}},
	}

	result.processMessages(messages)

	wantRules := []string{"alfa", "mike", "zulu"}
	if len(result.DetailsRuleFocused) != len(wantRules) {
		t.Fatalf("Expected %d rule details, got %d", len(wantRules), len(result.DetailsRuleFocused))
	}
	for i, want := range wantRules {
		if result.DetailsRuleFocused[i].Rule != want {
			t.Errorf("Rule at index %d: expected '%s', got '%s'", i, want, result.DetailsRuleFocused[i].Rule)
		}
	}

	alfa := result.DetailsRuleFocused[0]
	if len(alfa.Subjects) != 2 {
		t.Fatalf("Expected 2 subjects for rule 'alfa', got %d", len(alfa.Subjects))
	}
	if alfa.Subjects[0].Subject != "alpha.txt" || alfa.Subjects[1].Subject != "zebra.txt" {
		t.Errorf("Expected subjects [alpha.txt zebra.txt], got [%s %s]",
			alfa.Subjects[0].Subject, alfa.Subjects[1].Subject)
	}
}

func TestJSONStructureIntegrity(t *testing.T) {
	formatter := NewJSONFormatter()

	testFile := structs.File{
		Name: "integrity_test.go",
		Path: "/test/integrity_test.go",
	}

	messages := []structs.Message{
		{
			Content:  "Integrity test message",
			Source:   testFile,
			TestName: "IntegrityCheck",
		},
	}

	result, err := formatter.FormatResults("/test", "LocalCollector", messages, 1, []string{}, nil)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}

	// Verify the JSON is properly formatted and contains expected fields
	if !strings.Contains(result, "timestamp") {
		t.Error("JSON missing timestamp field")
	}

	if !strings.Contains(result, "scanned") {
		t.Error("JSON missing scanned field")
	}

	if !strings.Contains(result, "skipped") {
		t.Error("JSON missing skipped field")
	}

	if !strings.Contains(result, "details_subject_focused") {
		t.Error("JSON missing details_subject_focused field")
	}

	if !strings.Contains(result, "details_check_focused") {
		t.Error("JSON missing details_check_focused field")
	}

	if !strings.Contains(result, "errors") {
		t.Error("JSON missing errors field")
	}

	if !strings.Contains(result, "warnings") {
		t.Error("JSON missing warnings field")
	}
}

// TestFormatResultsCompact_SameDocumentWithoutIndentation pins that the compact
// variant the server puts on the wire carries none of the CLI output's
// indentation and is still a valid JSON document. Both variants render the same
// buildScanResult, so whitespace is all that can differ.
func TestFormatResultsCompact_SameDocumentWithoutIndentation(t *testing.T) {
	formatter := NewJSONFormatter()

	scanned := structs.File{Name: "data.csv", Path: "/data/data.csv"}
	oversized := structs.File{Name: "huge.bin", Path: "/data/huge.bin"}
	messages := []structs.Message{
		{
			Content:  "Found keyword 'password'",
			Source:   scanned,
			TestName: "IsFreeOfKeywords",
			Rules:    []string{"keywords-default"},
		},
		{
			Content:  "Skipped content scan of file: file too large.",
			Source:   oversized,
			TestName: "IsFreeOfKeywords",
			Skipped:  true,
			Reason:   "file too large",
		},
	}
	diagnostics := []structs.Diagnostic{
		{Level: structs.DiagWarning, Message: "a warning", Subject: "data.csv", Timestamp: "2026-08-26T00:00:00Z"},
	}
	pdfFiles := []string{"report.pdf"}

	indented, err := formatter.FormatResults("/data", "LocalCollector", messages, 2, pdfFiles, diagnostics)
	if err != nil {
		t.Fatalf("FormatResults failed: %v", err)
	}
	compact, err := formatter.FormatResultsCompact(messages, pdfFiles, diagnostics)
	if err != nil {
		t.Fatalf("FormatResultsCompact failed: %v", err)
	}

	// Self-check: without an indented reference the assertion below pins nothing.
	if !strings.Contains(indented, "\n  ") {
		t.Fatal("FormatResults is no longer indented")
	}
	if bytes.Contains(compact, []byte("\n  ")) {
		t.Error("FormatResultsCompact is indented; the server would put the whitespace on the wire")
	}

	var compactObj map[string]json.RawMessage
	if err := json.Unmarshal(compact, &compactObj); err != nil {
		t.Fatalf("compact result is not valid JSON: %v", err)
	}
}
