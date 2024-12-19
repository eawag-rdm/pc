package helpers

import (
	"fmt"
	"os"
)

// check file size and warn if it is too big
func WarnForLargeFile(filePath string, limitSize int64, message string) {
	// if the excel file is greater than 2MB warn the user, as it may cause performance issues
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		panic(err)
	}
	if fileInfo.Size() > limitSize {
		fmt.Println("Warning for file '" + fileInfo.Name() + "': " + message)
	}
}
