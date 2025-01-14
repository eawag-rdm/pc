package checks

import (
	"bytes"
	"os"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

/*
This file contains tests that need a collection of files. Eg: Checking if a repository has a readme file.
*/

const Readme_1 = "readme.md"
const Readme_2 = "readme.txt"

func isReadMe(file structs.File) bool {
	return strings.ToLower(file.Name) == Readme_1 || strings.ToLower(file.Name) == Readme_2
}

// Readme File is part of the package
func HasReadme(repository structs.Repository, config config.Config) []structs.Message {

	for _, file := range repository.Files {
		if isReadMe(file) {
			return nil
		}
	}
	return []structs.Message{{Content: "No ReadMe file in repository.", Source: repository}}
}

// Readme File is part of the package
func ReadMeContainsTOC(repository structs.Repository, config config.Config) []structs.Message {

	// check if the readme file is part of the repository
	var readmeFile = structs.File{}
	for _, file := range repository.Files {
		if isReadMe(file) {
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
		if !isReadMe(file) {
			if !bytes.Contains(content, []byte(file.Name)) {
				missing_files = append(missing_files, file.Name)
			}
		}
	}
	if len(missing_files) > 0 {
		return []structs.Message{{Content: "ReadMe file is missing a complete table of contents for this repository. Missing files are: '" + strings.Join(missing_files, "', '") + "'", Source: repository}}
	}
	return nil
}
