package config

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name               string
		configContent      string
		expectPanic        bool
		expectedTests      int
		expectedCollectors int
	}{
		{
			name: "valid config with blacklist",
			configContent: `
				[test.test1]
				blacklist = ["item1", "item2"]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectPanic:        false,
			expectedTests:      1,
			expectedCollectors: 1,
		},
		{
			name: "valid config with whitelist",
			configContent: `
				[test.test1]
				whitelist = ["item1", "item2"]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectPanic:        false,
			expectedTests:      1,
			expectedCollectors: 1,
		},
		{
			name: "invalid config with both blacklist and whitelist",
			configContent: `
				[test.test1]
				blacklist = ["item1"]
				whitelist = ["item2"]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectPanic:        true,
			expectedTests:      0,
			expectedCollectors: 0,
		},
		{
			name: "invalid config with neither blacklist nor whitelist",
			configContent: `
				[test.test1]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectPanic:        false,
			expectedTests:      1,
			expectedCollectors: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := os.CreateTemp("", "config-*.toml")
			if err != nil {
				t.Fatalf("failed to create temp file: %v", err)
			}
			defer os.Remove(file.Name())

			if _, err := file.WriteString(tt.configContent); err != nil {
				t.Fatalf("failed to write to temp file: %v", err)
			}
			file.Close()

			if tt.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Errorf("expected panic but did not occur")
					}
				}()
			}

			cfg := LoadConfig(file.Name())

			if !tt.expectPanic {
				if len(cfg.Tests) != tt.expectedTests {
					t.Errorf("expected %d tests to be loaded but got %d", tt.expectedTests, len(cfg.Tests))
				}
				if len(cfg.Collectors) != tt.expectedCollectors {
					t.Errorf("expected %d collectors to be loaded but got %d", tt.expectedCollectors, len(cfg.Collectors))
				}
			}
		})
	}
}

func TestAccessLoadedConfig(t *testing.T) {
	tests := []struct {
		name                            string
		configContent                   string
		expectAttrValue                 string
		expectedBlackListContent        []string
		expectedWhiteListContent        []string
		expectedKeywordArgumentsContent []map[string]string
	}{
		{
			name: "valid config with blacklist",
			configContent: `
				[test.test1]
				blacklist = ["item1", "item2"]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectAttrValue:                 "value1",
			expectedBlackListContent:        []string{"item1", "item2"},
			expectedWhiteListContent:        nil,
			expectedKeywordArgumentsContent: []map[string]string{{"arg1": "value1"}},
		},
		{
			name: "valid config with whitelist",
			configContent: `
				[test.test1]
				whitelist = ["item1", "item2"]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectAttrValue:                 "value1",
			expectedBlackListContent:        nil,
			expectedWhiteListContent:        []string{"item1", "item2"},
			expectedKeywordArgumentsContent: []map[string]string{{"arg1": "value1"}},
		},
		{
			name: "valid config with neither blacklist nor whitelist",
			configContent: `
				[test.test1]
				keywordArguments = [{ "arg1" = "value1" }]

				[collector.collector1]
				attrs = { "key1" = "value1" }
			`,
			expectAttrValue:                 "value1",
			expectedBlackListContent:        nil,
			expectedWhiteListContent:        nil,
			expectedKeywordArgumentsContent: []map[string]string{{"arg1": "value1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := os.CreateTemp("", "config-*.toml")
			if err != nil {
				t.Fatalf("failed to create temp file: %v", err)
			}
			defer os.Remove(file.Name())

			if _, err := file.WriteString(tt.configContent); err != nil {
				t.Fatalf("failed to write to temp file: %v", err)
			}
			file.Close()

			cfg := LoadConfig(file.Name())

			test, ok := cfg.Tests["test1"]
			if !ok {
				t.Fatalf("test1 not found in loaded config")
			}

			if tt.expectAttrValue != cfg.Collectors["collector1"].Attrs["key1"] {
				t.Errorf("expected collector attribute value %s but got %s", tt.expectAttrValue, cfg.Collectors["collector1"].Attrs["key1"])
			}

			if len(test.Blacklist) != len(tt.expectedBlackListContent) {
				t.Errorf("expected blacklist length %d but got %d", len(tt.expectedBlackListContent), len(test.Blacklist))
			} else {
				for i, v := range tt.expectedBlackListContent {
					if test.Blacklist[i] != v {
						t.Errorf("expected blacklist item %s but got %s", v, test.Blacklist[i])
					}
				}
			}

			if len(test.Whitelist) != len(tt.expectedWhiteListContent) {
				t.Errorf("expected whitelist length %d but got %d", len(tt.expectedWhiteListContent), len(test.Whitelist))
			} else {
				for i, v := range tt.expectedWhiteListContent {
					if test.Whitelist[i] != v {
						t.Errorf("expected whitelist item %s but got %s", v, test.Whitelist[i])
					}
				}
			}

			if len(test.KeywordArguments) != len(tt.expectedKeywordArgumentsContent) {
				t.Errorf("expected keyword arguments length %d but got %d", len(tt.expectedKeywordArgumentsContent), len(test.KeywordArguments))
			} else {
				for i, v := range tt.expectedKeywordArgumentsContent {
					for key, value := range v {
						if test.KeywordArguments[i][key] != value {
							t.Errorf("expected keyword argument %s=%s but got %s=%s", key, value, key, test.KeywordArguments[i][key])
						}
					}
				}
			}
		})
	}
}

func TestConfigFile(t *testing.T) {
	// Read the config file in testdata
	cfg := LoadConfig("../../testdata/config.toml.test")

	fmt.Println(cfg)
	// Check if the config file is loaded correctly
	assert.Equal(t, 3, len(cfg.Tests))
	assert.Equal(t, 1, len(cfg.Collectors))
}
