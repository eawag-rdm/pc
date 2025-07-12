package main

import (
	"encoding/json"
	"io"
	"log"
	"os"

	"github.com/eawag-rdm/pc/internal/tui"
)

func main() {
	var input io.Reader

	// Check if we're reading from stdin or a file
	if len(os.Args) > 1 {
		filename := os.Args[1]
		file, err := os.Open(filename)
		if err != nil {
			log.Fatalf("Error opening file %s: %v", filename, err)
		}
		defer file.Close()
		input = file
	} else {
		// Read from stdin
		input = os.Stdin
	}

	// Read JSON input
	data, err := io.ReadAll(input)
	if err != nil {
		log.Fatalf("Error reading input: %v", err)
	}

	// Parse JSON
	var scanResult tui.ScanResult
	if err := json.Unmarshal(data, &scanResult); err != nil {
		log.Fatalf("Error parsing JSON: %v", err)
	}

	// Start TUI
	app := tui.NewApp(&scanResult)
	if err := app.Run(); err != nil {
		log.Fatalf("Error running TUI: %v", err)
	}
}