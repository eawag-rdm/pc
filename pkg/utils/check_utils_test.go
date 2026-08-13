package utils

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/eawag-rdm/pc/pkg/config"
)

// TestApplyAllChecks_NoFilesNotice verifies both engine entrypoints append a
// single skip-style "no files to analyse" notice when the file set is empty, so
// the CLI and server surface the same clear, non-issue acknowledgement.
func TestApplyAllChecks_NoFilesNotice(t *testing.T) {
	plan := compilePlan(t, config.Config{})
	cases := map[string]func() []structs.Message{
		"ApplyAllChecks": func() []structs.Message {
			return ApplyAllChecks(context.Background(), config.Config{}, plan, nil, false)
		},
		"ApplyAllChecksWithProgress": func() []structs.Message {
			return ApplyAllChecksWithProgress(context.Background(), config.Config{}, plan, nil, false, nil)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			msgs := run()
			if len(msgs) != 1 {
				t.Fatalf("expected exactly the no-files notice for an empty set, got %d: %v", len(msgs), msgs)
			}
			notice := msgs[0]
			if !notice.Skipped {
				t.Error("no-files notice must be Skipped (a non-issue acknowledgement)")
			}
			if notice.TestName != "FilesPresent" {
				t.Errorf("expected TestName FilesPresent, got %q", notice.TestName)
			}
			if notice.Reason == "" || notice.Content == "" {
				t.Error("no-files notice must carry a reason and content")
			}
			if _, ok := notice.Source.(structs.Repository); !ok {
				t.Errorf("no-files notice should be repository-scoped, got %T", notice.Source)
			}
		})
	}
}

// TestApplyAllChecks_NilPlanPanics: a nil plan dispatches nothing and would
// report every package clean, so both engine entry points refuse it loudly.
func TestApplyAllChecks_NilPlanPanics(t *testing.T) {
	cases := map[string]func(){
		"ApplyAllChecks": func() {
			ApplyAllChecks(context.Background(), config.Config{}, nil, nil, false)
		},
		"ApplyAllChecksWithProgress": func() {
			ApplyAllChecksWithProgress(context.Background(), config.Config{}, nil, nil, false, nil)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("must panic on a nil plan")
				}
			}()
			run()
		})
	}
}

// admits compiles cfg and reports whether the file-scope rule of check admits
// the file. Selectors are never compiled at match time, so every filter test
// goes through the startup compile.
func admits(t *testing.T, cfg config.Config, check string, file structs.File) bool {
	t.Helper()
	return planRule(t, compilePlan(t, cfg), check, checks.ScopeFile).Match(file)
}

func TestRuleAdmitsFile(t *testing.T) {

	tests := []struct {
		name         string
		config       config.Config
		file         structs.File
		expectedSkip bool
	}{
		{
			name: "No whitelist or blacklist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name: "File in whitelist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Whitelist: []string{"test.txt"},
					},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name: "File in blacklist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Blacklist: []string{"txt"},
					},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: true,
		},
		{
			name: "File not in whitelist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Whitelist: []string{"other.txt"},
					},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: true,
		},
		{
			name: "File not in blacklist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Blacklist: []string{"other.txt"},
					},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name: "File matches whitelist regex",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Whitelist: []string{`.+\.txt`},
					},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name: "File matches blacklist regex",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Blacklist: []string{`.+\.txt`},
					},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: true,
		},
		{
			name: "File with space matches blacklist regex",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasOnlyASCII": {
						Blacklist: []string{`.+\.txt`},
					},
				},
			},
			file:         structs.File{Name: "test .txt"},
			expectedSkip: true,
		},
		{
			name: "Lists belong to a different check",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"IsValidName": {Whitelist: []string{"other.txt"}},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name: "Repository-scoped section is not a file filter",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"IsFreeOfSecrets": {Whitelist: []string{"other.txt"}},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name:         "No sections at all",
			config:       config.Config{},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := !admits(t, test.config, "HasOnlyASCII", test.file)
			if result != test.expectedSkip {
				t.Errorf("%v: rule skipped file = %v; want %v", test.name, result, test.expectedSkip)
			}
		})
	}
}

// TestMatchPatterns pins the pattern semantics of a compiled rule selector:
// unanchored RE2 over the file name, one compiled pattern per list entry (never
// a joined expression).
func TestMatchPatterns(t *testing.T) {
	tests := []struct {
		name          string
		list          []string
		str           string
		expectedMatch bool
	}{
		{
			name:          "Single pattern match",
			list:          []string{"test"},
			str:           "this is a test",
			expectedMatch: true,
		},
		{
			name:          "Single pattern no match",
			list:          []string{"test"},
			str:           "this is a sample",
			expectedMatch: false,
		},
		{
			name:          "Multiple patterns match",
			list:          []string{"test", "sample"},
			str:           "this is a sample",
			expectedMatch: true,
		},
		{
			name:          "Multiple patterns no match",
			list:          []string{"test", "example"},
			str:           "this is a sample",
			expectedMatch: false,
		},
		{
			name:          "Regex pattern match",
			list:          []string{`t.st`},
			str:           "this is a test",
			expectedMatch: true,
		},
		{
			name:          "Regex pattern no match",
			list:          []string{`t.st`},
			str:           "this is a sample",
			expectedMatch: false,
		},
		{
			name:          "Regex wildcard dot pattern match",
			list:          []string{".txt"},
			str:           "testfile.txt",
			expectedMatch: true,
		},
		{
			name:          "Regex character class pattern match",
			list:          []string{"t[a-z]t"},
			str:           "testfile.txt",
			expectedMatch: true,
		},
		{
			name:          "Regex character class pattern no match",
			list:          []string{"t[d-z]t"},
			str:           "abc",
			expectedMatch: false,
		},
		{
			// Per-pattern compilation scopes an inline flag to its own entry.
			// The joined regex leaked it into every later entry, so this list
			// used to admit "license.txt" as well.
			name:          "Inline flag does not leak into the next pattern",
			list:          []string{"(?i)readme", "LICENSE"},
			str:           "license.txt",
			expectedMatch: false,
		},
		{
			name:          "Inline flag applies to the pattern that spells it",
			list:          []string{"(?i)readme", "LICENSE"},
			str:           "README.md",
			expectedMatch: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Config{Tests: map[string]*config.TestConfig{
				"HasOnlyASCII": {Whitelist: test.list},
			}}
			result := admits(t, cfg, "HasOnlyASCII", structs.File{Name: test.str})
			if result != test.expectedMatch {
				t.Errorf("%v: selector match of %v against %q = %v; want %v", test.name, test.list, test.str, result, test.expectedMatch)
			}
		})
	}
}

// mockEntry wraps a plain per-file function as a one-rule plan entry - the shape
// every dispatch function takes - so a dispatch test can exercise the dispatch
// rather than a real check.
func mockEntry(name string, run func(structs.File) []structs.Message) checkRules {
	def := checks.CheckDef{
		Name: name,
		// The dispatch asserts the scope it hands RunFile against these, so a
		// mock has to declare the phases the dispatch tests drive it through.
		Scopes: checks.ScopesOf(checks.ScopeFile, checks.ScopeArchiveFileList, checks.ScopeArchiveMember),
		RunFile: func(_ context.Context, file structs.File, _ checks.Scope, _ *checks.Batch, rules []*checks.BoundRule) []structs.Message {
			var messages []structs.Message
			for range rules {
				messages = append(messages, run(file)...)
			}
			return messages
		},
	}
	return checkRules{
		def:   &def,
		batch: &checks.Batch{},
		rules: []*checks.BoundRule{{Rule: name}},
	}
}

func mockCheckPass(file structs.File) []structs.Message {
	return []structs.Message{{Content: "Check passed"}}
}

func mockCheckFail(file structs.File) []structs.Message {
	return []structs.Message{{Content: "Check failed"}}
}

func TestApplyChecksFilteredByFile(t *testing.T) {
	// A real file-scope check, on a name it would otherwise flag: an empty
	// result proves the filter, not a silent check.
	whitespaceEntries := func(t *testing.T, lists config.TestConfig) []checkRules {
		t.Helper()
		cfg := config.Config{Tests: map[string]*config.TestConfig{"HasNoWhiteSpace": &lists}}
		plan := compilePlan(t, cfg)
		for _, entry := range plan.scope(checks.ScopeFile) {
			if entry.def.Name == "HasNoWhiteSpace" {
				return []checkRules{entry}
			}
		}
		t.Fatal("HasNoWhiteSpace has no file-scope rule")
		return nil
	}

	tests := []struct {
		name     string
		entries  []checkRules
		files    []structs.File
		expected []structs.Message
	}{
		{
			name:     "Single file, single check pass",
			entries:  []checkRules{mockEntry("mockCheckPass", mockCheckPass)},
			files:    []structs.File{{Name: "test.txt"}},
			expected: []structs.Message{{Content: "Check passed", TestName: "mockCheckPass"}},
		},
		{
			name:     "Single file, single check fail",
			entries:  []checkRules{mockEntry("mockCheckFail", mockCheckFail)},
			files:    []structs.File{{Name: "test.txt"}},
			expected: []structs.Message{{Content: "Check failed", TestName: "mockCheckFail"}},
		},
		{
			name: "Multiple files, multiple checks",
			entries: []checkRules{
				mockEntry("mockCheckPass", mockCheckPass),
				mockEntry("mockCheckFail", mockCheckFail),
			},
			files: []structs.File{{Name: "test1.txt"}, {Name: "test2.txt"}},
			expected: []structs.Message{
				{Content: "Check passed", TestName: "mockCheckPass"},
				{Content: "Check failed", TestName: "mockCheckFail"},
				{Content: "Check passed", TestName: "mockCheckPass"},
				{Content: "Check failed", TestName: "mockCheckFail"},
			},
		},
		{
			name:     "Check skipped due to whitelist",
			entries:  whitespaceEntries(t, config.TestConfig{Whitelist: []string{"other.txt"}}),
			files:    []structs.File{{Name: "test file.txt"}},
			expected: []structs.Message{},
		},
		{
			name:     "Check skipped due to blacklist",
			entries:  whitespaceEntries(t, config.TestConfig{Blacklist: []string{"test file.txt"}}),
			files:    []structs.File{{Name: "test file.txt"}},
			expected: []structs.Message{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := applyChecksFilteredByFile(context.Background(), test.entries, test.files)
			if !reflect.DeepEqual(result, test.expected) {
				t.Errorf("%v: applyChecksFilteredByFile() = %v; want %v", test.name, result, test.expected)
			}
		})
	}
}

// buildNamedZip writes a zip whose members all carry a check-triggering name.
func buildNamedZip(t *testing.T, names []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "members.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create member: %v", err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatalf("write member: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	return path
}

// TestArchiveFileListChecks_WalkCapSkipsArchive: an archive past the member
// limit yields exactly one skip acknowledgement and NO checks over its partial
// member list; the same archive under the limit is checked normally.
func TestArchiveFileListChecks_WalkCapSkipsArchive(t *testing.T) {
	path := buildNamedZip(t, []string{"a file.txt", "b file.txt", "c file.txt"})
	archive := structs.File{Path: path, Name: "members.zip", DisplayName: "members.zip", IsArchive: true}
	plan := compilePlan(t, config.Config{})
	var nameChecks []checkRules
	for _, entry := range plan.scope(checks.ScopeArchiveFileList) {
		if entry.def.Name == "HasNoWhiteSpace" {
			nameChecks = append(nameChecks, entry)
		}
	}

	const memberLimit = 2
	capped := config.Config{General: &config.GeneralConfig{MaxArchiveMemberCount: memberLimit}}
	msgs := applyChecksFilteredByFileOnArchiveFileList(context.Background(), capped, nameChecks, []structs.File{archive})
	if len(msgs) != 1 {
		t.Fatalf("expected exactly the archive skip acknowledgement, got %d: %v", len(msgs), msgs)
	}
	if !msgs[0].Skipped || msgs[0].Reason == "" {
		t.Errorf("walk-cap message must be a skip acknowledgement with a reason: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Reason, fmt.Sprintf("more than %d members", memberLimit)) {
		t.Errorf("skip reason must name the configured member limit: %q", msgs[0].Reason)
	}
	if src, ok := msgs[0].Source.(structs.File); !ok || src.Name != archive.Name {
		t.Errorf("walk-cap message must be attributed to the archive, got %+v", msgs[0].Source)
	}

	// Same archive, limit above the member count: the member names are checked.
	uncapped := config.Config{General: &config.GeneralConfig{MaxArchiveMemberCount: 10}}
	msgs = applyChecksFilteredByFileOnArchiveFileList(context.Background(), uncapped, nameChecks, []structs.File{archive})
	if len(msgs) != 3 {
		t.Fatalf("expected one whitespace issue per member, got %d: %v", len(msgs), msgs)
	}
}
