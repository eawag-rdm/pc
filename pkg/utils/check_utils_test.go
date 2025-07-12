package utils

import (
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/eawag-rdm/pc/pkg/config"
)

func TestGetFunctionName(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected string
	}{
		{input: getFunctionName, expected: "getFunctionName"},
		{input: reflect.ValueOf, expected: "ValueOf"},
	}

	for _, test := range tests {
		result := getFunctionName(test.input)
		if result != test.expected {
			t.Errorf("getFunctionName(%v) = %v; want %v", test.input, result, test.expected)
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
			name: "File matches blacklist regex",
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
			name:          "Regex pattern match",
			list:          []string{".txt"},
			str:           "testfile.txt",
			expectedMatch: true,
		},
		{
			name:          "Regex pattern match",
			list:          []string{"t[a-z]t"},
			str:           "testfile.txt",
			expectedMatch: true,
		},
		{
			name:          "Regex pattern no match",
			list:          []string{"t[d-z]t"},
			str:           "abc",
			expectedMatch: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := matchPatterns(test.list, test.str)
			if result != test.expectedMatch {
				t.Errorf("%v: matchPatterns(%v, %v) = %v; want %v", test.name, test.list, test.str, result, test.expectedMatch)
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
			result := ApplyChecksFilteredByFile(test.config, test.checks, test.files)
			if !reflect.DeepEqual(result, test.expected) {
				t.Errorf("%v: ApplyChecksFilteredByFile() = %v; want %v", test.name, result, test.expected)
			}
		})
	}
}
