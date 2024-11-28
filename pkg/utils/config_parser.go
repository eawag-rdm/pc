package utils

import (
	toml "github.com/pelletier/go-toml"
)

func parseTOML(file string) (map[string]interface{}, error) {
	var config map[string]interface{}
	if configTree, err := toml.LoadFile(file); err != nil {
		return nil, err
	} else {
		config = configTree.ToMap()
	}
	return config, nil
}

// define a format definition for the config file
// each section must resemble a test
// the section will be called after the testname
// for each test (section) there's a configuration
// each test fill have a blacklist and whitelist
// if both lists are empty, all files will be checked
// if the whitelist is empty, all files will be checked except the ones in the blacklist
// if the blacklist is empty, only the files in the whitelist will be checked
// other parameters like a keyword list will be defined in the test section and passed to the test function
// the test function will be called with the testname and the configuration
// the test function will return a list of files that failed the test
type Config struct {
	Tests map[string]Test `toml:"tests"`
}

type Test struct {
	Blacklist []string          `toml:"blacklist"`
	Whitelist []string          `toml:"whitelist"`
	Keywords  map[string]string `toml:"keywords"`
}

// maps loading toml to config struct
func LoadConfig(file string) (Config, error) {
	var config Config
	if configTree, err := toml.LoadFile(file); err != nil {
		return config, err
	} else {
		err = configTree.Unmarshal(&config)
		if err != nil {
			return config, err
		}
	}
	return config, nil
}
