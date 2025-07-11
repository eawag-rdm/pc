package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Structures for final parsed configuration
type TestConfig struct {
	Blacklist        []string
	Whitelist        []string
	KeywordArguments []map[string]interface{}
}

type CollectorConfig struct {
	Attrs map[string]interface{}
}

type OperationConfig struct {
	Collector string
}

type GeneralConfig struct {
	MaxMessagesPerType     int
	MaxArchiveFileSize     int64 // Maximum size for individual files in archives (bytes)
	MaxTotalArchiveMemory  int64 // Maximum total memory for archive processing (bytes)
}

type Config struct {
	General    *GeneralConfig
	Tests      map[string]*TestConfig
	Operation  map[string]*OperationConfig
	Collectors map[string]*CollectorConfig
}

// ParseConfigNew parses the TOML file into a ConfigNew structure
func ParseConfig(filename string) (*Config, error) {
	var raw map[string]interface{}
	if _, err := toml.DecodeFile(filename, &raw); err != nil {
		return nil, err
	}

	c := &Config{
		General: &GeneralConfig{
			MaxMessagesPerType:    10,                    // Default value
			MaxArchiveFileSize:    10 * 1024 * 1024,     // 10MB default
			MaxTotalArchiveMemory: 100 * 1024 * 1024,    // 100MB default
		},
		Tests:      map[string]*TestConfig{},
		Operation:  map[string]*OperationConfig{},
		Collectors: map[string]*CollectorConfig{},
	}

	parseStringSlice := func(data []interface{}) []string {
		var result []string
		for _, item := range data {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}

	parseKeywordArguments := func(data []interface{}) []map[string]interface{} {
		var result []map[string]interface{}
		for _, kwItem := range data {
			if kwMap, ok := kwItem.(map[string]interface{}); ok {
				kwSet := make(map[string]interface{})
				for k, v := range kwMap {
					switch val := v.(type) {
					case string:
						kwSet[k] = val
					case []interface{}:
						kwSet[k] = parseStringSlice(val)
					}
				}
				result = append(result, kwSet)
			}
		}
		return result
	}

	// Parse general section
	if generalData, ok := raw["general"].(map[string]interface{}); ok {
		if maxMsgs, ok := generalData["maxMessagesPerType"].(int64); ok {
			c.General.MaxMessagesPerType = int(maxMsgs)
		}
		if maxArchiveFileSize, ok := generalData["maxArchiveFileSize"].(int64); ok {
			c.General.MaxArchiveFileSize = maxArchiveFileSize
		}
		if maxTotalArchiveMemory, ok := generalData["maxTotalArchiveMemory"].(int64); ok {
			c.General.MaxTotalArchiveMemory = maxTotalArchiveMemory
		}
	}

	if testData, ok := raw["test"].(map[string]interface{}); ok {
		for name, section := range testData {
			tc := &TestConfig{}
			if sectionMap, ok := section.(map[string]interface{}); ok {
				if bl, ok := sectionMap["blacklist"].([]interface{}); ok {
					tc.Blacklist = parseStringSlice(bl)
				}
				if wl, ok := sectionMap["whitelist"].([]interface{}); ok {
					tc.Whitelist = parseStringSlice(wl)
				}
				if kwArgs, ok := sectionMap["keywordArguments"].([]interface{}); ok {
					tc.KeywordArguments = parseKeywordArguments(kwArgs)
				}
			}
			c.Tests[name] = tc
		}
	}

	if collectorData, ok := raw["collector"].(map[string]interface{}); ok {
		for name, section := range collectorData {
			cc := &CollectorConfig{Attrs: make(map[string]interface{})}
			if sectionMap, ok := section.(map[string]interface{}); ok {
				if attrs, ok := sectionMap["attrs"].(map[string]interface{}); ok {
					for k, v := range attrs {
						switch val := v.(type) {
						case string:
							cc.Attrs[k] = val
						case bool:
							cc.Attrs[k] = val
						case []interface{}:
							cc.Attrs[k] = parseStringSlice(val)
						}
					}
				}
			}
			c.Collectors[name] = cc
		}
	}

	if operationData, ok := raw["operation"].(map[string]interface{}); ok {
		for name, section := range operationData {
			oc := &OperationConfig{}
			if sectionMap, ok := section.(map[string]interface{}); ok {
				if collector, ok := sectionMap["collector"].(string); ok {
					oc.Collector = collector
				}
			}
			c.Operation[name] = oc
		}
	}
	return c, nil
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
func LoadConfig(file string) (*Config, error) {
	var config *Config
	config, err := ParseConfig(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file '%s': %w", file, err)
	}

	for testName, test := range config.Tests {
		if err := assesLists(test.Blacklist, test.Whitelist); err != nil {
			return nil, fmt.Errorf("error in test %s: %v", testName, err)
		}
	}

	return config, nil
}

// check fore the default configurtion file 1. ~/.config/pc/config.toml 2. ./config.toml if exists return the path
func FindConfigFile() string {
	// check for the default configuration file
	// 1. ~/.config/pc/config.toml
	// 2. ./config.toml
	paths := []string{
		"~/pc.toml",
		"./pc.toml",
		"~/.config/pc.toml",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""

}
