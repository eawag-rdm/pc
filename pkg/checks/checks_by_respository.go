package checks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
)

/*
This file contains tests that need a collection of files. Eg: Checking if a repository has a readme file.
*/

// defaultReadmeNames is the readme filename list a rule that configures none is
// bound to. The check's defaults live in its own Bind - default-rule synthesis
// relies on exactly that - and a readme check with NO names would be incoherent
// (it could never find a readme).
var defaultReadmeNames = []string{
	"readme.md", "readme.txt", "readme",
	"read me", "read me.txt", "read me.md",
	"read-me", "read-me.txt", "read-me.md",
	"read_me", "read_me.txt", "read_me.md",
}

// bindReadmeNames type-checks the readme filename list of a rule's parameter
// sets (readme_names), once, at load. HasReadme and ReadMeContainsTOC bind the
// SAME list: "what counts as the readme" must have exactly one definition, so
// checks.RuleSpecs feeds both from one declaration (shareReadmeNames).
func bindReadmeNames(spec config.RuleSpec) ([]string, error) {
	sets, err := ruleSets(spec, "readme_names")
	if err != nil {
		return nil, err
	}
	var names []string
	for i, set := range sets {
		configured, err := stringList(set, "readme_names", i)
		if err != nil {
			return nil, err
		}
		if len(configured) == 0 {
			return nil, fmt.Errorf("parameter set %d: %q must be a non-empty list", i+1, "readme_names")
		}
		names = append(names, configured...)
	}
	if len(names) == 0 {
		return defaultReadmeNames, nil
	}
	return names, nil
}

func bindHasReadme(spec config.RuleSpec, _ *config.GeneralConfig) (*BoundRule, error) {
	names, err := bindReadmeNames(spec)
	if err != nil {
		return nil, err
	}
	return &BoundRule{
		Rule: spec.Name,
		applyRepo: func(_ context.Context, repository structs.Repository, _ *Batch, _ *selector.Selector) []structs.Message {
			return hasReadme(repository, names)
		},
	}, nil
}

func bindReadMeContainsTOC(spec config.RuleSpec, _ *config.GeneralConfig) (*BoundRule, error) {
	names, err := bindReadmeNames(spec)
	if err != nil {
		return nil, err
	}
	return &BoundRule{
		Rule: spec.Name,
		applyRepo: func(_ context.Context, repository structs.Repository, _ *Batch, _ *selector.Selector) []structs.Message {
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
		output.GlobalLogger.FileWarning(readmeFile.GetDisplayName(), "Error reading ReadMe file '%s': %v", readmeFile.Path, err)
		reason := "Skipped table-of-contents check: the ReadMe could not be read."
		return []structs.Message{{Content: reason, Source: readmeFile, Skipped: true, Reason: reason}}
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
