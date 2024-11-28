package checks

import (
	"os"
	"testing"
)

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func tempFile(content []byte) string {
	file, err := os.CreateTemp("", "go-testing")
	check(err)
	_, err = file.Write(content)
	check(err)
	return file.Name()
}

func TestIsAscii(t *testing.T) {
	// Test cases
	var asciiTests = []struct {
		char     byte // input
		expected bool // expected result
	}{
		{'d', true},
		{'ä', false},
		{'Ç', false},
		{'_', true},
		{':', true},
		{' ', true},
		{'$', true},
	}

	// Loop over test cases
	for _, tt := range asciiTests {
		is_ASCII := isASCII(tt.char) // Call the function being tested
		if is_ASCII != tt.expected {
			t.Errorf("Error for '%c': got %v, want %v", tt.char, is_ASCII, tt.expected)
		}
	}
}

func TestIsSpace(t *testing.T) {
	// Test cases
	var spaceTests = []struct {
		char     byte // input
		expected bool // expected result
	}{
		{' ', true},
		{'4', false},
	}

	// Loop over test cases
	for _, tt := range spaceTests {
		is_Space := isSpace(tt.char) // Call the function being tested
		if is_Space != tt.expected {
			t.Errorf("Error for '%c': got %v, want %v", tt.char, is_Space, tt.expected)
		}
	}
}

func TestIsBinaryFile(t *testing.T) {
	// Test cases
	var binTests = []struct {
		file     string // input
		expected bool   // expected result
	}{
		{tempFile([]byte{101, 111, 0, 222}), true},
		{tempFile([]byte("Hello")), false},
	}

	// Loop over test cases
	for _, tt := range binTests {
		isBin, _ := isBinaryFile(tt.file) // Call the function being tested
		if isBin != tt.expected {
			t.Errorf("Error for '%v': got %v, want %v", tt.file, isBin, tt.expected)
		}
	}
}
