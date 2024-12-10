package config

import (
	"fmt"

	"github.com/pelletier/go-toml"
)

// Config represents the structure of the configuration file
type Config struct {
	Tests map[string]Test `toml:"tests"`
}

// Test represents the structure of each test in the configuration
type Test struct {
	Blacklist []string            `toml:"blacklist"`
	Whitelist []string            `toml:"whitelist"`
	Keywords  []map[string]string `toml:"keywordArguments"`
}

// assesLists checks that there is no overlap between blacklist and whitelist
// and ensures that only one of the two is defined
func assesLists(blacklist []string, whitelist []string) error {
	if !((len(blacklist) > 0 && len(whitelist) == 0) ||
		(len(blacklist) == 0 && len(whitelist) > 0) ||
		(len(blacklist) == 0 && len(whitelist) == 0)) {
		return fmt.Errorf("only one is allowed to have entries. Either the blacklist OR the whitelist")
	}
	return nil
}

// LoadConfig loads the configuration from a TOML file and performs the necessary checks
func LoadConfig(file string) (Config, error) {
	var config Config
	configTree, err := toml.LoadFile(file)
	if err != nil {
		panic(err)
	}

	err = configTree.Unmarshal(&config)
	if err != nil {
		panic(err)
	}

	for testName, test := range config.Tests {
		if err := assesLists(test.Blacklist, test.Whitelist); err != nil {
			return config, fmt.Errorf("error in test %s: %v", testName, err)
		}
	}

	return config, nil
}

// CollectCKANFiles config struct
type CKANConfig struct {
	CKANURL   string
	PackageID string
}

// LoadConfig loads the configuration from a TOML file and performs the necessary checks
func LoadCKANConfig(file string) (CKANConfig, error) {
	var config CKANConfig
	configTree, err := toml.LoadFile(file)
	if err != nil {
		panic(err)
	}

	err = configTree.Unmarshal(&config)
	if err != nil {
		panic(err)
	}

	return config, nil
}
