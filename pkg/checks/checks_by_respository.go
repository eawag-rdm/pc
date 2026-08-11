package checks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

/*
This file contains tests that need a collection of files. Eg: Checking if a repository has a readme file.
*/

// bindReadmeNames type-checks the readme filename list of a rule's parameter
// sets (keywordArguments readme_names), once, at load. HasReadme and
// ReadMeContainsTOC bind the SAME list: "what counts as the readme" must have
// exactly one definition, so the translation feeds both from one section.
func bindReadmeNames(spec config.RuleSpec) ([]string, error) {
	sets, err := paramSets(spec)
	if err != nil {
		return nil, err
	}
	var names []string
	for i, set := range sets {
		configured, err := stringList(set, "readme_names", i)
		if err != nil {
			return nil, err
		}
		names = append(names, configured...)
	}
	return names, nil
}

func bindHasReadme(spec config.RuleSpec, general *config.GeneralConfig) (*BoundRule, error) {
	names, err := bindReadmeNames(spec)
	if err != nil {
		return nil, err
	}
	return &BoundRule{
		Rule: spec.Name,
		applyRepo: func(repository structs.Repository, _ *Batch, _ *selector.Selector) []structs.Message {
			return hasReadme(repository, names)
		},
	}, nil
}

func bindReadMeContainsTOC(spec config.RuleSpec, general *config.GeneralConfig) (*BoundRule, error) {
	names, err := bindReadmeNames(spec)
	if err != nil {
		return nil, err
	}
	return &BoundRule{
		Rule: spec.Name,
		applyRepo: func(repository structs.Repository, _ *Batch, _ *selector.Selector) []structs.Message {
			return readMeContainsTOC(repository, names)
		},
	}, nil
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
func hasReadme(repository structs.Repository, names []string) []structs.Message {
	for _, file := range repository.Files {
		if isReadMe(file, names) {
			return nil
		}
	}
	return []structs.Message{{Content: "No ReadMe file in repository.", Source: repository}}
}

// Readme File is part of the package
func readMeContainsTOC(repository structs.Repository, names []string) []structs.Message {
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
