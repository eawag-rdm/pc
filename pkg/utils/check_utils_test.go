package utils

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/eawag-rdm/pc/pkg/config"
)

// TestApplyAllChecks_NoFilesNotice verifies both engine entrypoints append a
// single skip-style "no files to analyse" notice when the file set is empty, so
// the CLI and server surface the same clear, non-issue acknowledgement.
func TestApplyAllChecks_NoFilesNotice(t *testing.T) {
	cases := map[string]func() []structs.Message{
		"ApplyAllChecks": func() []structs.Message {
			return ApplyAllChecks(context.Background(), config.Config{}, CheckSelectors{}, nil, false)
		},
		"ApplyAllChecksWithProgress": func() []structs.Message {
			return ApplyAllChecksWithProgress(context.Background(), config.Config{}, CheckSelectors{}, nil, false, nil)
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

func TestGetFunctionName(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected string
	}{
		{input: optimization.FunctionName, expected: "FunctionName"},
		{input: reflect.ValueOf, expected: "ValueOf"},
	}

	for _, test := range tests {
		result := optimization.FunctionName(test.input)
		if result != test.expected {
			t.Errorf("FunctionName(%v) = %v; want %v", test.input, result, test.expected)
		}
	}
}

// compileSelectors builds the startup selector table the dispatch functions
// take. Selectors are never compiled at match time, so every filter test goes
// through this constructor.
func compileSelectors(t *testing.T, cfg config.Config) CheckSelectors {
	t.Helper()
	selectors, err := CompileCheckSelectors(cfg)
	if err != nil {
		t.Fatalf("compile check selectors: %v", err)
	}
	return selectors
}

// filterFor compiles cfg and resolves it for a single check, exactly as every
// dispatch function does once before its per-file loop.
func filterFor(t *testing.T, cfg config.Config, check func(file structs.File, config config.Config) []structs.Message) []*selector.Selector {
	t.Helper()
	return compileSelectors(t, cfg).resolve([]func(file structs.File, config config.Config) []structs.Message{check})
}

func TestSkipFileCheck(t *testing.T) {

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
			result := skipFileCheck(filterFor(t, test.config, checks.HasOnlyASCII), 0, test.file)
			if result != test.expectedSkip {
				t.Errorf("%v: skipFileCheck() = %v; want %v", test.name, result, test.expectedSkip)
			}
		})
	}
}

// TestSkipFileCheck_ArchiveKeywordAlias pins the one hard-coded identity alias:
// IsArchiveFreeOfKeywords is filtered by [test.IsFreeOfKeywords] (it dies with
// the reflection-derived check identity in R7).
func TestSkipFileCheck_ArchiveKeywordAlias(t *testing.T) {
	cfg := config.Config{Tests: map[string]*config.TestConfig{
		"IsFreeOfKeywords": {Blacklist: []string{`\.zip$`}},
	}}

	sels := filterFor(t, cfg, checks.IsArchiveFreeOfKeywords)
	if !skipFileCheck(sels, 0, structs.File{Name: "data.zip"}) {
		t.Error("IsArchiveFreeOfKeywords must be filtered by the IsFreeOfKeywords section")
	}
	if skipFileCheck(sels, 0, structs.File{Name: "data.tar"}) {
		t.Error("a name outside the blacklist must not be skipped")
	}
}

// TestCompileCheckSelectors_BadPatternsFailAtBoot pins the new contract: a
// pattern that does not compile - or a section that sets both lists, whose
// precedence changed - fails the startup compile instead of silently filtering
// everything (whitelist) or nothing (blacklist). All faults are reported at
// once, each named with its [test.X] section.
func TestCompileCheckSelectors_BadPatternsFailAtBoot(t *testing.T) {
	cases := map[string]config.Config{
		"uncompilable whitelist": {Tests: map[string]*config.TestConfig{
			"HasOnlyASCII": {Whitelist: []string{"("}},
		}},
		"uncompilable blacklist": {Tests: map[string]*config.TestConfig{
			"HasOnlyASCII": {Blacklist: []string{"[a-"}},
		}},
		"empty pattern": {Tests: map[string]*config.TestConfig{
			"HasOnlyASCII": {Blacklist: []string{"keep.txt", ""}},
		}},
		"both lists set": {Tests: map[string]*config.TestConfig{
			"HasOnlyASCII": {Whitelist: []string{"keep.txt"}, Blacklist: []string{"drop.txt"}},
		}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			selectors, err := CompileCheckSelectors(cfg)
			if err == nil {
				t.Fatal("expected a load error")
			}
			if len(selectors.byCheck) != 0 {
				t.Error("a failed compile must yield no selector table")
			}
			var compileErr *selector.CompileError
			if !errors.As(err, &compileErr) {
				t.Fatalf("error must carry a *selector.CompileError, got %T: %v", err, err)
			}
			if compileErr.Rule != "HasOnlyASCII" {
				t.Errorf("fault must name its config section, got %q", compileErr.Rule)
			}
		})
	}

	// Every faulty section is reported, not just the first.
	many := config.Config{Tests: map[string]*config.TestConfig{
		"HasNoWhiteSpace": {Whitelist: []string{"("}},
		"IsValidName":     {Blacklist: []string{"["}},
		"HasOnlyASCII":    {Blacklist: []string{`\.txt$`}},
	}}
	_, err := CompileCheckSelectors(many)
	if err == nil {
		t.Fatal("expected a load error")
	}
	for _, want := range []string{`"HasNoWhiteSpace"`, `"IsValidName"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregated error must name section %s: %v", want, err)
		}
	}
}

// TestCompileCheckSelectors_IgnoresNonFileSections pins the scope of the boot
// gate: only sections that file dispatch filters on are compiled. Repository-
// scoped checks own their lists (leakcheck compiles its own filter and tolerates
// patterns this constructor rejects), so a pattern under [test.IsFreeOfSecrets]
// must not refuse the boot - nor become a file filter.
func TestCompileCheckSelectors_IgnoresNonFileSections(t *testing.T) {
	cfg := config.Config{Tests: map[string]*config.TestConfig{
		"IsFreeOfSecrets": {Blacklist: []string{"", "x("}},
		"HasReadme":       {Whitelist: []string{"["}},
	}}
	selectors, err := CompileCheckSelectors(cfg)
	if err != nil {
		t.Fatalf("a repository-scoped section must not fail the boot compile: %v", err)
	}
	if len(selectors.byCheck) != 0 {
		t.Errorf("repository-scoped sections must not become file filters, got %d entries", len(selectors.byCheck))
	}
}

// TestMatchPatterns pins the pattern semantics of a compiled check selector:
// unanchored RE2 over the file name, one compiled pattern per list entry (never
// a joined expression). Expressed through skipFileCheck, where a whitelist miss
// is a skip.
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
			sels := filterFor(t, config.Config{Tests: map[string]*config.TestConfig{
				"HasOnlyASCII": {Whitelist: test.list},
			}}, checks.HasOnlyASCII)
			result := !skipFileCheck(sels, 0, structs.File{Name: test.str})
			if result != test.expectedMatch {
				t.Errorf("%v: selector match of %v against %q = %v; want %v", test.name, test.list, test.str, result, test.expectedMatch)
			}
		})
	}
}
func mockCheckPass(file structs.File, config config.Config) []structs.Message {
	return []structs.Message{{Content: "Check passed"}}
}

func mockCheckFail(file structs.File, config config.Config) []structs.Message {
	return []structs.Message{{Content: "Check failed"}}
}

func TestApplyChecksFilteredByFile(t *testing.T) {
	tests := []struct {
		name     string
		config   config.Config
		checks   []func(file structs.File, config config.Config) []structs.Message
		files    []structs.File
		expected []structs.Message
	}{
		{
			name: "Single file, single check pass",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"mockCheckPass": {},
				},
			},
			checks:   []func(file structs.File, config config.Config) []structs.Message{mockCheckPass},
			files:    []structs.File{{Name: "test.txt"}},
			expected: []structs.Message{{Content: "Check passed", TestName: "mockCheckPass"}},
		},
		{
			name: "Single file, single check fail",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"mockCheckFail": {},
				},
			},
			checks:   []func(file structs.File, config config.Config) []structs.Message{mockCheckFail},
			files:    []structs.File{{Name: "test.txt"}},
			expected: []structs.Message{{Content: "Check failed", TestName: "mockCheckFail"}},
		},
		{
			name: "Multiple files, multiple checks",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"mockCheckPass": {},
					"mockCheckFail": {},
				},
			},
			checks: []func(file structs.File, config config.Config) []structs.Message{mockCheckPass, mockCheckFail},
			files:  []structs.File{{Name: "test1.txt"}, {Name: "test2.txt"}},
			expected: []structs.Message{
				{Content: "Check passed", TestName: "mockCheckPass"},
				{Content: "Check failed", TestName: "mockCheckFail"},
				{Content: "Check passed", TestName: "mockCheckPass"},
				{Content: "Check failed", TestName: "mockCheckFail"},
			},
		},
		{
			// A real file-scope check, on a name it would otherwise flag: an
			// empty result proves the filter, not a silent check.
			name: "Check skipped due to whitelist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasNoWhiteSpace": {
						Whitelist: []string{"other.txt"},
					},
				},
			},
			checks:   []func(file structs.File, config config.Config) []structs.Message{checks.HasNoWhiteSpace},
			files:    []structs.File{{Name: "test file.txt"}},
			expected: []structs.Message{},
		},
		{
			name: "Check skipped due to blacklist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"HasNoWhiteSpace": {
						Blacklist: []string{"test file.txt"},
					},
				},
			},
			checks:   []func(file structs.File, config config.Config) []structs.Message{checks.HasNoWhiteSpace},
			files:    []structs.File{{Name: "test file.txt"}},
			expected: []structs.Message{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := applyChecksFilteredByFile(context.Background(), test.config, compileSelectors(t, test.config), test.checks, test.files)
			if !reflect.DeepEqual(result, test.expected) {
				t.Errorf("%v: ApplyChecksFilteredByFile() = %v; want %v", test.name, result, test.expected)
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
	nameChecks := []func(file structs.File, config config.Config) []structs.Message{checks.HasNoWhiteSpace}

	const memberLimit = 2
	capped := config.Config{General: &config.GeneralConfig{MaxArchiveMemberCount: memberLimit}}
	msgs := applyChecksFilteredByFileOnArchiveFileList(context.Background(), capped, CheckSelectors{}, nameChecks, []structs.File{archive})
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
	msgs = applyChecksFilteredByFileOnArchiveFileList(context.Background(), uncapped, CheckSelectors{}, nameChecks, []structs.File{archive})
	if len(msgs) != 3 {
		t.Fatalf("expected one whitespace issue per member, got %d: %v", len(msgs), msgs)
	}
}
