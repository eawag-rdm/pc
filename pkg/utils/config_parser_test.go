package utils

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	// Create a temporary TOML file for testing
	tempFile, err := os.CreateTemp("", "config_*.toml")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	// Write sample TOML content to the temporary file
	tomlContent := `
	[tests.test1]
	blacklist = ["item1", "item2"]
	whitelist = []
	keywords = [{ key1 = "value1", key2 = "value2" }]

	[tests.test2]
	blacklist = []
	whitelist = ["item3", "item4"]
	keywords = [{ key3 = "value3", key4 = "value4" }]
	`
	_, err = tempFile.WriteString(tomlContent)
	assert.NoError(t, err)
	tempFile.Close()

	// Load the configuration from the temporary file
	config, err := LoadConfig(tempFile.Name())
	assert.NoError(t, err)

	// Validate the loaded configuration
	assert.Len(t, config.Tests, 2)

	test1 := config.Tests["test1"]
	assert.ElementsMatch(t, test1.Blacklist, []string{"item1", "item2"})
	assert.Empty(t, test1.Whitelist)
	assert.Equal(t, []map[string]string{{"key1": "value1", "key2": "value2"}}, test1.Keywords)

	test2 := config.Tests["test2"]
	assert.Empty(t, test2.Blacklist)
	assert.ElementsMatch(t, test2.Whitelist, []string{"item3", "item4"})
	assert.Equal(t, []map[string]string{{"key3": "value3", "key4": "value4"}}, test2.Keywords)
}

func TestLoadConfigWithInvalidLists(t *testing.T) {
	// Create a temporary TOML file for testing
	tempFile, err := os.CreateTemp("", "config_invalid_*.toml")
	assert.NoError(t, err)
	defer os.Remove(tempFile.Name())

	// Write sample TOML content with invalid lists to the temporary file
	tomlContent := `
	[tests.test1]
	blacklist = ["item1"]
	whitelist = ["item2"]
	keywords = [{ key1 = "value1" }]
	`
	_, err = tempFile.WriteString(tomlContent)
	assert.NoError(t, err)
	tempFile.Close()

	// Load the configuration from the temporary file
	_, err = LoadConfig(tempFile.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "only one is allowed to have entries")
}
