package utils

import (
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
