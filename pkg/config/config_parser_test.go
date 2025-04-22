package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func createTempConfigFile(t *testing.T, content string) string {
	tmpfile, err := os.CreateTemp("", "config-*.toml")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	return tmpfile.Name()
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		configData  string
		expectPanic bool
	}{
		{
			name: "ValidConfig",
			configData: `
				[operation]
				collector = "collector1"

				[test.test1]
				blacklist = ["item1", "item2"]

				[collector.collector1.attrs]
				attr1 = "value1"
			`,
			expectPanic: false,
		},
		{
			name: "InvalidConfigBothLists",
			configData: `
				[test.test1]
				blacklist = ["item1"]
				whitelist = ["item2"]
			`,
			expectPanic: true,
		},
		{
			name: "InvalidConfigNoLists",
			configData: `
				[test.test1]
			`,
			expectPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := createTempConfigFile(t, tt.configData)
			defer os.Remove(configFile)

			if tt.expectPanic {
				assert.Panics(t, func() { LoadConfig(configFile) })
			} else {
				assert.NotPanics(t, func() { LoadConfig(configFile) })
				config := LoadConfig(configFile)
				assert.NotNil(t, config)
			}
		})
	}
}

func TestConfigFile(t *testing.T) {
	// Read the config file in testdata
	cfg, err := ParseConfig("../../testdata/test_config.toml")
	if err != nil {
		t.Fatal(err)
	}
	// Check if the config file is loaded correctly
	assert.Equal(t, 3, len(cfg.Tests))
	assert.Equal(t, 2, len(cfg.Collectors))

	keywords, ok := (*cfg.Tests["IsFreeOfKeywords"]).KeywordArguments[2]["keywords"].([]string)
	assert.True(t, ok)
	assert.Equal(t, 1, len(keywords))
	assert.Contains(t, keywords, "/Users/")

}

func TestParseConfig(t *testing.T) {
	// Create a temporary TOML file for testing
	tomlContent := `
	[operation.main]
	collector = "collector1"

	[test.test1]
	blacklist = ["item1", "item2"]
	whitelist = ["item3"]
	keywordArguments = [{ "arg1" = "value1" }, {"arg1" = "value1", "arg2" = ["value2", "value3"] }]

	[test.test2]
	keywordArguments = [{"arg1" = "value1", "arg2" = ["/path/", "C:/path/"] }]

	[collector.collector1]
	attrs = { "key1" = "value1", "key2" = ["value2", "value3"] }
	`
	tmpFile, err := os.CreateTemp("", "test_config_*.toml")
	assert.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.Write([]byte(tomlContent))
	assert.NoError(t, err)
	tmpFile.Close()

	// Parse the temporary TOML file
	config, err := ParseConfig(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, err)
	assert.NotNil(t, config)

	// Validate the parsed data
	testConfig, ok := config.Tests["test1"]
	assert.True(t, ok)
	assert.ElementsMatch(t, []string{"item1", "item2"}, testConfig.Blacklist)
	assert.ElementsMatch(t, []string{"item3"}, testConfig.Whitelist)
	assert.Len(t, testConfig.KeywordArguments, 2)
	assert.ElementsMatch(t, []string{"value2", "value3"}, testConfig.KeywordArguments[1]["arg2"])
	assert.Equal(t, "item1", testConfig.Blacklist[0])
	assert.Equal(t, "value1", testConfig.KeywordArguments[0]["arg1"])

	testConfig2, ok := config.Tests["test2"]
	assert.True(t, ok)
	assert.Len(t, testConfig2.KeywordArguments, 1)
	assert.Equal(t, "value1", testConfig2.KeywordArguments[0]["arg1"])
	assert.ElementsMatch(t, []string{"/path/", "C:/path/"}, testConfig2.KeywordArguments[0]["arg2"])
	assert.Equal(t, 2, len(testConfig2.KeywordArguments[0]["arg2"].([]string)))

	collectorConfig, ok := config.Collectors["collector1"]
	assert.True(t, ok)
	assert.Equal(t, "value1", collectorConfig.Attrs["key1"])
	assert.ElementsMatch(t, []string{"value2", "value3"}, collectorConfig.Attrs["key2"])

	operationConfig, ok := config.Operation["main"]
	assert.True(t, ok)
	assert.Equal(t, "collector1", operationConfig.Collector)

}

func TestAssesLists(t *testing.T) {
	tests := []struct {
		blacklist []string
		whitelist []string
		expectErr bool
	}{
		{[]string{"item1"}, []string{}, false},
		{[]string{}, []string{"item1"}, false},
		{[]string{}, []string{}, false},
		{[]string{"item1"}, []string{"item2"}, true},
		{[]string{"item1"}, []string{"item1"}, true},
	}

	for _, tt := range tests {
		err := assesLists(tt.blacklist, tt.whitelist)
		if tt.expectErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}
