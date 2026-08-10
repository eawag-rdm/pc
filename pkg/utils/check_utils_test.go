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
	"github.com/eawag-rdm/pc/pkg/optimization"
	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/eawag-rdm/pc/pkg/config"
)

// TestApplyAllChecks_NoFilesNotice verifies both engine entrypoints append a
// single skip-style "no files to analyse" notice when the file set is empty, so
// the CLI and server surface the same clear, non-issue acknowledgement.
func TestApplyAllChecks_NoFilesNotice(t *testing.T) {
	cases := map[string]func() []structs.Message{
		"ApplyAllChecks": func() []structs.Message {
			return ApplyAllChecks(context.Background(), config.Config{}, nil, false)
		},
		"ApplyAllChecksWithProgress": func() []structs.Message {
			return ApplyAllChecksWithProgress(context.Background(), config.Config{}, nil, false, nil)
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

func mockCheck(file structs.File, config config.Config) []structs.Message { return nil }
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
					"mockCheck": {},
				},
			},
			file:         structs.File{Name: "test.txt"},
			expectedSkip: false,
		},
		{
			name: "File in whitelist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"mockCheck": {
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
					"mockCheck": {
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
					"mockCheck": {
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
					"mockCheck": {
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
					"mockCheck": {
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
					"mockCheck": {
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
					"mockCheck": {
						Blacklist: []string{`.+\.txt`},
					},
				},
			},
			file:         structs.File{Name: "test .txt"},
			expectedSkip: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {

			result := skipFileCheck(test.config, mockCheck, test.file)
			if result != test.expectedSkip {
				t.Errorf("%v: skipFileCheck() = %v; want %v", test.name, result, test.expectedSkip)
			}
		})
	}
}
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := matchRegexPatterns(test.list, test.str)
			if result != test.expectedMatch {
				t.Errorf("%v: matchRegexPatterns(%v, %v) = %v; want %v", test.name, test.list, test.str, result, test.expectedMatch)
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
			name: "Check skipped due to whitelist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"mockCheckPass": {
						Whitelist: []string{"other.txt"},
					},
				},
			},
			checks:   []func(file structs.File, config config.Config) []structs.Message{mockCheckPass},
			files:    []structs.File{{Name: "test.txt"}},
			expected: []structs.Message{},
		},
		{
			name: "Check skipped due to blacklist",
			config: config.Config{
				Tests: map[string]*config.TestConfig{
					"mockCheckPass": {
						Blacklist: []string{"test.txt"},
					},
				},
			},
			checks:   []func(file structs.File, config config.Config) []structs.Message{mockCheckPass},
			files:    []structs.File{{Name: "test.txt"}},
			expected: []structs.Message{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ApplyChecksFilteredByFile(context.Background(), test.config, test.checks, test.files)
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
	msgs := ApplyChecksFilteredByFileOnArchiveFileList(context.Background(), capped, nameChecks, []structs.File{archive})
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
	msgs = ApplyChecksFilteredByFileOnArchiveFileList(context.Background(), uncapped, nameChecks, []structs.File{archive})
	if len(msgs) != 3 {
		t.Fatalf("expected one whitespace issue per member, got %d: %v", len(msgs), msgs)
	}
}
