package helpers

import (
	"fmt"
	"os"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// check file size and warn if it is too big
func WarnForLargeFile(file structs.File, limitSize int64, message string) {
	// if the excel file is greater than 2MB warn the user, as it may cause performance issues
	fileInfo, err := os.Stat(file.Path)
	if err != nil {
		panic(err)
	}
	if fileInfo.Size() > limitSize {
		fmt.Println("Warning for file '" + file.Name + "': " + message)
	}
}
