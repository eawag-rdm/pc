package checks

import (
	"bufio"
	"os"
	"unicode"
)

// isBinaryFile checks if a file is likely a binary or unreadable file.
func isBinaryFile(filePath string) (bool, error) {
	// Open the file for reading
	file, err := os.Open(filePath)
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Read a small sample of the file
	const sampleSize = 512
	reader := bufio.NewReader(file)
	buffer := make([]byte, sampleSize)

	n, err := reader.Read(buffer)
	if err != nil && err.Error() != "EOF" {
		return false, err
	}

	// Analyze the sample for non-printable characters
	for i := 0; i < n; i++ {
		// Allow common printable characters (ASCII and some Unicode spaces)
		if !unicode.IsPrint(rune(buffer[i])) && !unicode.IsSpace(rune(buffer[i])) {
			return true, nil // Non-printable character found, likely binary
		}
	}
	return false, nil // All characters are printable, likely not binary
}

//
