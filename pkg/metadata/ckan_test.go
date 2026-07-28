package metadata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func loadResult(t *testing.T, file string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read fixture %s: %v", file, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse fixture %s: %v", file, err)
	}
	result, ok := doc["result"].(map[string]any)
	if !ok {
		t.Fatalf("fixture %s missing 'result' object", file)
	}
	return result
}

func countByTest(msgs []structs.Message) map[string]int {
	out := map[string]int{}
	for _, m := range msgs {
		out[m.TestName]++
	}
	return out
}

// package_show.json — a "complete" publication package; all resources public.
func TestCkanComplete(t *testing.T) {
	m := CkanMetadataFromJSON(loadResult(t, "package_show.json"))
	if len(m.Entities) != 21 { // 1 package + 20 resources
		t.Fatalf("expected 21 entities, got %d", len(m.Entities))
	}
	by := countByTest(RunChecks(m))
	// First author "Contzen, Nadja" has no email.
	if by["AuthorFormat"] != 1 {
		t.Errorf("expected 1 AuthorFormat failure, got %d", by["AuthorFormat"])
	}
	// This package has no usage_contact field.
	if by["Required"] != 1 {
		t.Errorf("expected 1 Required failure (usage_contact), got %d", by["Required"])
	}
	// status "complete" and every resource "public".
	if by["Equals"] != 0 {
		t.Errorf("expected 0 Equals failures, got %d", by["Equals"])
	}
	if by["TitleFormat"] != 0 {
		t.Errorf("expected 0 TitleFormat failures, got %d", by["TitleFormat"])
	}
}

// package_show_incomplete.json — an "incomplete" package with restricted resources.
func TestCkanIncomplete(t *testing.T) {
	m := CkanMetadataFromJSON(loadResult(t, "package_show_incomplete.json"))
	if len(m.Entities) != 8 { // 1 package + 7 resources
		t.Fatalf("expected 8 entities, got %d", len(m.Entities))
	}
	by := countByTest(RunChecks(m))
	// Title does not start with "Data for: ".
	if by["TitleFormat"] != 1 {
		t.Errorf("expected 1 TitleFormat failure, got %d", by["TitleFormat"])
	}
	// status "incomplete" (1) + 6 resources "same_organization" (6).
	if by["Equals"] != 7 {
		t.Errorf("expected 7 Equals failures, got %d", by["Equals"])
	}
	// reviewed_by is empty.
	if by["Required"] != 1 {
		t.Errorf("expected 1 Required failure (reviewed_by), got %d", by["Required"])
	}
	// First author "Kollmann, Josianne <...>" is well-formed.
	if by["AuthorFormat"] != 0 {
		t.Errorf("expected 0 AuthorFormat failures, got %d", by["AuthorFormat"])
	}
}
