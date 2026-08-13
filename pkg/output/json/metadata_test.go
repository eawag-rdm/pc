package json

import (
	"encoding/json"
	"testing"

	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// TestFormatResultsMetadataSection verifies that messages whose Source is a
// *metadata.Entity are routed into details_metadata and kept out of the file,
// subject, and check buckets.
func TestFormatResultsMetadataSection(t *testing.T) {
	pkgEntity := &metadata.Entity{Kind: "package", Name: "pkg-x"}
	resEntity := &metadata.Entity{Kind: "resource", Name: "data.csv"}
	file := structs.ToFile("/tmp/f.txt", "f.txt", 10, ".txt")

	messages := []structs.Message{
		{Content: "title bad", Source: pkgEntity, TestName: "TitleFormat"},
		{Content: "author bad", Source: pkgEntity, TestName: "AuthorFormat"},
		{Content: "not public", Source: resEntity, TestName: "Equals"},
		{Content: "file issue", Source: file, TestName: "HasOnlyASCII"},
	}

	out, err := NewJSONFormatter().FormatResults("loc", "CkanCollector", messages, 1, nil, nil)
	if err != nil {
		t.Fatalf("FormatResults: %v", err)
	}

	var sr ScanResult
	if err := json.Unmarshal([]byte(out), &sr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Two metadata entities, in encounter order, package first with 2 issues.
	if len(sr.DetailsMetadata) != 2 {
		t.Fatalf("expected 2 metadata entities, got %d", len(sr.DetailsMetadata))
	}
	if sr.DetailsMetadata[0].Kind != "package" || sr.DetailsMetadata[0].Name != "pkg-x" {
		t.Errorf("unexpected first entity: %+v", sr.DetailsMetadata[0])
	}
	if len(sr.DetailsMetadata[0].Issues) != 2 {
		t.Errorf("expected 2 issues on the package entity, got %d", len(sr.DetailsMetadata[0].Issues))
	}
	if sr.DetailsMetadata[1].Kind != "resource" {
		t.Errorf("expected the resource entity second, got %q", sr.DetailsMetadata[1].Kind)
	}

	// The file message still produces a scanned file.
	if len(sr.Scanned) != 1 {
		t.Errorf("expected 1 scanned file, got %d", len(sr.Scanned))
	}

	// Metadata checks must not leak into the check-focused view.
	for _, cd := range sr.DetailsCheckFocused {
		switch cd.Checkname {
		case "TitleFormat", "AuthorFormat", "Equals":
			t.Errorf("metadata check %q leaked into DetailsCheckFocused", cd.Checkname)
		}
	}
}
