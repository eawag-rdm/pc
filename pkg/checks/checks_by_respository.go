package checks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

/*
This file contains tests that need a collection of files. Eg: Checking if a repository has a readme file.
*/

// readmeNames returns the readme filename list from the REQUIRED
// [test.HasReadme] section (keywordArguments readme_names). Like the other
// config-driven checks, presence and shape are guaranteed by
// config.ValidateChecksConfig at boot/startup; SafeRun guards the runtime.
// ReadMeContainsTOC shares this list deliberately: "what counts as the
// readme" must have exactly one definition.
func readmeNames(cfg config.Config) []string {
	var names []string
	for _, args := range cfg.Tests["HasReadme"].KeywordArguments {
		if configured, ok := args["readme_names"].([]string); ok {
			names = append(names, configured...)
		}
	}
	return names
}

// isReadMe reports whether file's name matches one of the readme names
// (case-insensitive, full-filename match).
func isReadMe(file structs.File, names []string) bool {
	lower := strings.ToLower(file.Name)
	for _, name := range names {
		if lower == strings.ToLower(name) {
			return true
		}
	}
	return false
}

// Readme File is part of the package
func HasReadme(repository structs.Repository, config config.Config) []structs.Message {
	names := readmeNames(config)
	for _, file := range repository.Files {
		if isReadMe(file, names) {
			return nil
		}
	}
	return []structs.Message{{Content: "No ReadMe file in repository.", Source: repository}}
}

// Readme File is part of the package
func ReadMeContainsTOC(repository structs.Repository, config config.Config) []structs.Message {
	names := readmeNames(config)

	// check if the readme file is part of the repository
	var readmeFile = structs.File{}
	for _, file := range repository.Files {
		if isReadMe(file, names) {
			readmeFile = file
		}
	}

	// if no readme, the check is not applicable
	if (structs.File{}) == readmeFile {
		return nil
	}

	// read the content of the readme file
	content, err := os.ReadFile(readmeFile.Path)

	if err != nil {
		panic(err)
	}

	missing_files := []string{}
	for _, file := range repository.Files {
		if !isReadMe(file, names) {
			nameWithoutSuffix := strings.TrimSuffix(file.Name, filepath.Ext(file.Name))
			if !bytes.Contains(content, []byte(nameWithoutSuffix)) {
				missing_files = append(missing_files, file.Name)
			}
		}
	}
	if len(missing_files) > 0 {
		return []structs.Message{{Content: "ReadMe file is missing a complete table of contents for this repository. Missing files are: '" + strings.Join(missing_files, "', '") + "'", Source: repository}}
	}
	return nil
}
