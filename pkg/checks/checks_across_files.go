package checks

import (
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"
)

const Readme_1 = "readme.md"
const Readme_2 = "readme.txt"

// Readme File is part of the package
func HasReadme(file structs.File) bool {
	return strings.ToLower(file.Name) == Readme_1 || strings.ToLower(file.Name) == Readme_2
}
