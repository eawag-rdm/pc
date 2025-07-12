package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"

	"github.com/eawag-rdm/pc/internal/tui"
	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

func main() {

	// implement small cli to call pc with config and a folder (both can have default args)
	// then the files will be collected with the local_collector and the checks will be applied
	// the results will be printed to the console
	// the exit code will be 0 if no errors were found, otherwise 1
	// the cli should have a help command to show the usage

	// Define default values for the config and folder arguments
	defaultConfig := config.FindConfigFile()
	// current word directory
	defaultFolder := "."

	// Parse CLI arguments
	cfg := flag.String("config", defaultConfig, "Path to the config file")
	folder_or_url := flag.String("location", defaultFolder, "Path to local folder or CKAN package name. It depends on the set collector.")
	help := flag.Bool("help", false, "Show usage information")
	jsonOutput := flag.Bool("json", false, "Output results in JSON format for visualization tools")
	tuiOutput := flag.Bool("tui", false, "Launch interactive TUI viewer after scan")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile := flag.String("memprofile", "", "write memory profile to file")
	flag.Parse()
	
	// Configure logger for output mode immediately after parsing flags
	// Use JSON mode for both --json and --tui flags
	output.GlobalLogger.SetJSONMode(*jsonOutput || *tuiOutput)
	
	// Enable CPU profiling if requested
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}

	if *help {
		flag.Usage()
		return
	}

	generalConfig, err := config.LoadConfig(*cfg)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	var (
		files    []structs.File
		filesErr error
	)

	// Decide which collector to use
	if generalConfig.Operation["main"].Collector == "LocalCollector" {
		files, filesErr = collectors.LocalCollector(*folder_or_url, *generalConfig)
		if filesErr != nil {
			fmt.Printf("Error: %v\n", filesErr)
			return
		}

	} else if generalConfig.Operation["main"].Collector == "CkanCollector" {
		if *folder_or_url == "." {
			fmt.Println("Please provide a CKAN package name (use the location flag '-location')")
			return
		}
		files, filesErr = collectors.CkanCollector(*folder_or_url, *generalConfig)
		if filesErr != nil {
			fmt.Printf("Error: %v\n", filesErr)
			return
		}

	} else {
		fmt.Println("Unknown collector")
		return
	}

	// Check if we found any files to process
	if len(files) == 0 {
		fmt.Printf("No files found in location: %s\n", *folder_or_url)
		return
	}

	messages := utils.ApplyAllChecks(*generalConfig, files, true)
	
	// Output results in requested format
	if *jsonOutput || *tuiOutput {
		// Create JSON formatter
		formatter := output.NewJSONFormatter()
		
		// Get collector name from config
		collectorName := generalConfig.Operation["main"].Collector
		
		jsonResult, err := formatter.FormatResults(*folder_or_url, collectorName, messages, len(files))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting JSON output: %v\n", err)
			return
		}
		
		if *tuiOutput {
			// Parse JSON for TUI
			var scanResult tui.ScanResult
			if err := json.Unmarshal([]byte(jsonResult), &scanResult); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing JSON for TUI: %v\n", err)
				return
			}
			
			// Launch TUI
			app := tui.NewApp(&scanResult)
			if err := app.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
				return
			}
		} else {
			// Just output JSON
			fmt.Println(jsonResult)
		}
	} else {
		// Traditional text output
		if len(messages) > 0 {
			fmt.Println("=== Results ===")
			for _, message := range messages {
				fmt.Println(message.Format())
			}
		}
		fmt.Println(helpers.PDFTracker.FormatFiles())
	}
	
	// Enable memory profiling if requested
	if *memprofile != "" {
		f, err := os.Create(*memprofile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatal(err)
		}
	}
}
