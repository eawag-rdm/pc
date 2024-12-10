package utils

import (
	"reflect"
	"testing"
)

func TestSkipCheck(t *testing.T) {
	tests := []struct {
		name      string
		config    Config
		checkName string
		file      File
		want      bool
	}{
		{
			name:      "No config for check",
			config:    Config{},
			checkName: "IsASCII",
			file:      File{Name: "test.txt"},
			want:      false,
		},
		{
			name: "File in whitelist",
			config: Config{
				Tests: map[string]Test{
					"IsASCII": {
						Whitelist: []string{".+.txt"},
						Blacklist: []string{},
						Keywords:  []map[string]string{},
					},
				},
			},
			checkName: "IsASCII",
			file:      File{Name: "test.txt"},
			want:      false,
		},
		{
			name: "File in blacklist",
			config: Config{
				Tests: map[string]Test{
					"IsASCII": {
						Whitelist: []string{},
						Blacklist: []string{".+.txt"},
						Keywords:  []map[string]string{},
					},
				},
			},
			checkName: "IsASCII",
			file:      File{Name: "test.txt"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipCheck(tt.config, tt.checkName, tt.file); got != tt.want {
				t.Errorf("skipCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyChecksFiltered(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		checks map[string]reflect.Value
		files  []File
		want   []Message
	}{
		{
			name:   "No checks to apply",
			config: Config{},
			checks: map[string]reflect.Value{},
			files:  []File{{Name: "test.txt"}},
			want:   nil,
		},
		{
			name: "Apply check to file",
			config: Config{
				Tests: map[string]Test{
					"HasOnlyASCII": {
						Whitelist: []string{".+.txt"},
						Blacklist: []string{},
						Keywords:  []map[string]string{},
					},
				},
			},
			checks: map[string]reflect.Value{
				"HasOnlyASCII": reflect.ValueOf(func(file File) Message {
					return Message{Content: "Check passed"}
				}),
			},
			files: []File{{Name: "test.txt"}},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyChecksFiltered(tt.config, tt.checks, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ApplyChecksFiltered() = %v, want %v", got, tt.want)
			}
		})
	}
}
