package checks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// errAfter reports itself cancelled from the n-th Err() call on. A scan walks
// an archive in ONE goroutine, so counting the cancellation points it passes
// cuts the scan at an exact place - where a deadline would race the machine.
type errAfter struct {
	context.Context
	calls int
	limit int
}

func (c *errAfter) Err() error {
	c.calls++
	if c.calls > c.limit {
		return context.Canceled
	}
	return nil
}

// TestKeywordScanStopsOnCancellation pins the ctx exits of the keyword scan -
// the archive-member walk and the body-entry loop - through the exported
// RunFile contract. Cutting the scan at every cancellation point in turn, the
// findings must always be a subset of the full run's, and at least one cut must
// leave a STRICT, non-empty subset: that is the whole contract at once - the
// scan stops early, and what it found before stopping stands. Delete either
// ctx.Err() and the strict-subset case disappears.
func TestKeywordScanStopsOnCancellation(t *testing.T) {
	cfg, err := config.LoadConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	for i := range cfg.Rules {
		if cfg.Rules[i].Check == "IsFreeOfKeywords" {
			cfg.Rules[i].Include, cfg.Rules[i].Exclude = nil, nil
		}
	}

	// one_of_each holds the keyword "password" in more than one member, so a cut
	// between members is observable as a shorter finding list.
	archive := structs.File{Path: "../../testdata/archives/one_of_each.zip", Name: "one_of_each.zip", DisplayName: "one_of_each.zip", IsArchive: true}
	def, rules, batch := bindTestRule(t, "IsFreeOfKeywords", *cfg, ScopeArchiveMember)

	findings := func(ctx context.Context) []string {
		var out []string
		for _, message := range def.RunFile(ctx, archive, ScopeArchiveMember, batch, rules) {
			if message.Skipped {
				continue // iterator acknowledgements are not findings
			}
			out = append(out, message.Content)
		}
		return out
	}

	full := findings(context.Background())
	if len(full) < 2 {
		t.Fatalf("fixture must yield findings from at least two members for the cut to be observable, got %v", full)
	}
	expected := make(map[string]int, len(full))
	for _, finding := range full {
		expected[finding]++
	}

	// One cancellation point per member plus one per (keyword set, body entry);
	// this bound clears the fixture's members with room for more keyword sets.
	sawStrictSubset := false
	for limit := 1; limit <= 4*len(full)+16; limit++ {
		got := findings(&errAfter{Context: context.Background(), limit: limit})
		if len(got) > len(full) {
			t.Fatalf("cut at %d produced MORE findings (%d) than the uncancelled scan (%d)", limit, len(got), len(full))
		}
		seen := make(map[string]int, len(got))
		for _, finding := range got {
			seen[finding]++
			if seen[finding] > expected[finding] {
				t.Errorf("cut at %d invented a finding absent from the full scan: %q", limit, finding)
			}
			if strings.Contains(strings.ToLower(finding), "context canceled") {
				t.Errorf("cut at %d surfaced cancellation as a finding: %q", limit, finding)
			}
		}
		if len(got) > 0 && len(got) < len(full) {
			sawStrictSubset = true
		}
	}
	if !sawStrictSubset {
		t.Fatal("no cut yielded a strict non-empty subset: the ctx.Err() exits in the archive walk and the keyword scan are unobservable, so deleting them would keep this suite green")
	}

	// The strict subset above proves the SCAN stops finding; it does not prove
	// the WALK stops, because a scan that returns nothing per member looks the
	// same from the findings. So cut at the first member and count the
	// cancellation points the run consults. Measured on this fixture: 3 with the
	// member-loop break, 6 with it removed (the walk hands over every remaining
	// member and the scan consults ctx once for each). The budget sits between,
	// so deleting that break fails this test.
	const walkStopBudget = 4
	cut := &errAfter{Context: context.Background(), limit: 1}
	findings(cut)
	if cut.calls > walkStopBudget {
		t.Errorf("the archive walk consulted ctx %d times after being cancelled at its first member (budget %d): it kept walking instead of breaking out", cut.calls, walkStopBudget)
	}
}

// TestScanKeywordsStopsBetweenBodyEntries pins the SECOND cancellation point,
// the one the archive test cannot reach: an archive hands over one body entry
// per member, so only a multi-entry body (OOXML blocks, PDF pages) exercises
// the loop inside scanKeywords. Cut it after the second entry and the later
// entries' findings must be absent while the earlier ones stand.
func TestScanKeywordsStopsBetweenBodyEntries(t *testing.T) {
	body := [][]byte{
		[]byte("page one mentions a password"),
		[]byte("page two mentions a password"),
		[]byte("page three mentions a password"),
		[]byte("page four mentions a password"),
	}
	sets := []keywordSet{{matcher: optimization.GetMatcher([]string{"password"}), info: "Possible credentials in file"}}

	full := scanKeywords(context.Background(), sets, body, lowerAll(body), reportPaged)
	if len(full) != len(body) {
		t.Fatalf("expected one finding per body entry, got %d for %d entries", len(full), len(body))
	}

	// limit 2: the first two Err() calls report alive, so entries 0 and 1 scan
	// and the third call cuts the loop.
	cut := scanKeywords(&errAfter{Context: context.Background(), limit: 2}, sets, body, lowerAll(body), reportPaged)
	if len(cut) != 2 {
		t.Errorf("cancelled scan returned %d findings, want the 2 collected before the cut: %v", len(cut), cut)
	}
	for i, message := range cut {
		if message.Content != full[i].Content {
			t.Errorf("finding %d differs from the uncancelled scan: %q vs %q", i, message.Content, full[i].Content)
		}
	}
}

// TestStreamChunksStopsBetweenChunks pins the THIRD cancellation point: a file
// too large to hold is read chunk by chunk, and a fired ctx must stop the read
// rather than stream the whole file to a scan that no longer reports anything.
func TestStreamChunksStopsBetweenChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(path, bytes.Repeat([]byte("a"), 2*streamChunkSize+512), 0o600); err != nil {
		t.Fatalf("write streaming fixture: %v", err)
	}

	chunks := func(ctx context.Context) int {
		scanned := 0
		if err := streamChunks(ctx, path, func(chunk, lowered []byte) { scanned++ }); err != nil {
			t.Fatalf("streamChunks: %v", err)
		}
		return scanned
	}

	full := chunks(context.Background())
	if full < 2 {
		t.Fatalf("fixture must span at least two chunks, got %d", full)
	}
	if cut := chunks(&errAfter{Context: context.Background(), limit: 1}); cut != 1 {
		t.Errorf("cancelled read scanned %d chunks, want the 1 taken before the cut (uncancelled: %d)", cut, full)
	}
}

// TestNameChecksIgnoreCancellation pins the other half of the documented
// contract: ctx is an UPPER bound, and the checks that read no content are
// entitled to ignore it. A pre-cancelled context must not silence them.
func TestNameChecksIgnoreCancellation(t *testing.T) {
	cfg, err := config.LoadConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	file := structs.File{Name: "Bad Name!.txt", Path: "Bad Name!.txt", DisplayName: "Bad Name!.txt"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	def, rules, batch := bindTestRule(t, "HasNoWhiteSpace", *cfg, ScopeFile)
	if messages := def.RunFile(ctx, file, ScopeFile, batch, rules); len(messages) == 0 {
		t.Error("a cancelled context silenced a name check; the contract says name checks ignore ctx")
	}
}
