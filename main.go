package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"time"

	"github.com/eawag-rdm/pc/pkg/output/tui"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
	htmlformatter "github.com/eawag-rdm/pc/pkg/output/html"
	plainformatter "github.com/eawag-rdm/pc/pkg/output/plain"
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
	tuiOutput := flag.Bool("tui", false, "Launch interactive TUI viewer after scan")
	htmlOutput := flag.String("html", "", "Generate static HTML report to specified file (e.g., --html report.html)")
	plainOutput := flag.Bool("plain", false, "Output a concise plain text summary instead of JSON")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile := flag.String("memprofile", "", "write memory profile to file")
	flag.Parse()
	
	// Configure logger for JSON mode by default
	output.GlobalLogger.SetJSONMode(true)
	
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
		// Output config error in JSON format
		errorResult := map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"error": map[string]string{
				"type": "config_error",
				"message": fmt.Sprintf("Error loading config: %v", err),
			},
		}
		if jsonBytes, marshalErr := json.MarshalIndent(errorResult, "", "  "); marshalErr == nil {
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Printf("{\"error\": \"Error loading config: %v\"}\n", err)
		}
		return
	}

	var (
		files    []structs.File
		filesErr error
	)

	// Helper function to output error in JSON format
	outputError := func(errorType, message string) {
		errorResult := map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"error": map[string]string{
				"type":    errorType,
				"message": message,
			},
		}
		if jsonBytes, marshalErr := json.MarshalIndent(errorResult, "", "  "); marshalErr == nil {
			fmt.Println(string(jsonBytes))
		} else {
			fmt.Printf("{\"error\": \"%s\"}\n", message)
		}
	}

	// Decide which collector to use
	if generalConfig.Operation["main"].Collector == "LocalCollector" {
		files, filesErr = collectors.LocalCollector(*folder_or_url, *generalConfig)
		if filesErr != nil {
			outputError("collector_error", filesErr.Error())
			return
		}

	} else if generalConfig.Operation["main"].Collector == "CkanCollector" {
		if *folder_or_url == "." {
			outputError("collector_error", "Please provide a CKAN package name (use the location flag '-location')")
			return
		}
		files, filesErr = collectors.CkanCollector(*folder_or_url, *generalConfig)
		if filesErr != nil {
			outputError("collector_error", filesErr.Error())
			return
		}

	} else {
		outputError("collector_error", "Unknown collector")
		return
	}

	// Check if we found any files to process
	if len(files) == 0 {
		outputError("no_files", fmt.Sprintf("No files found in location: %s", *folder_or_url))
		return
	}
	

	if *tuiOutput {
		// Launch TUI with live scanning
		app := tui.NewScanningApp()
		
		// Channel for scan completion
		scanComplete := make(chan *tui.ScanResult)
		scanErrors := make(chan error)
		
		// Set up startup callback to begin scanning
		app.SetStartupCallback(func() {
			// Start scanning in a goroutine
			go func() {
				defer func() {
					if r := recover(); r != nil {
						scanErrors <- fmt.Errorf("scan panic: %v", r)
					}
				}()
				
				// Update progress to show scanning started
				app.UpdateProgress(0, 1, "Starting scan...")
				
				// Run scanning with progress updates
				messages := utils.ApplyAllChecksWithProgress(*generalConfig, files, true, func(current, total int, message string) {
					app.UpdateProgress(current, total, message)
				})
				
				// Create JSON formatter and generate output
				formatter := jsonformatter.NewJSONFormatter()
				
				// Get collector name from config
				collectorName := generalConfig.Operation["main"].Collector
				
				jsonResult, err := formatter.FormatResults(*folder_or_url, collectorName, messages, len(files), helpers.PDFTracker.Files)
				if err != nil {
					scanErrors <- fmt.Errorf("formatting error: %v", err)
					return
				}
				
				// Parse JSON for TUI
				var scanResult tui.ScanResult
				if err := json.Unmarshal([]byte(jsonResult), &scanResult); err != nil {
					scanErrors <- fmt.Errorf("JSON parsing error: %v", err)
					return
				}
				
				// Send results
				scanComplete <- &scanResult
			}()
			
			// Handle scan completion
			go func() {
				select {
				case result := <-scanComplete:
					app.UpdateData(result)
					// Progress should show total tests run (including skipped tests)
					// This will be handled by the final callback from ApplyAllChecksWithProgress
				case err := <-scanErrors:
					app.UpdateProgress(0, 1, fmt.Sprintf("Scan failed: %v", err))
				}
			}()
		})
		
		// Run TUI (this blocks until user exits)
		if err := app.Run(); err != nil {
			outputError("tui_error", fmt.Sprintf("Error running TUI: %v", err))
			return
		}
	} else if *htmlOutput != "" {
		// Generate HTML report
		messages := utils.ApplyAllChecks(*generalConfig, files, true)
		
		// Create JSON formatter and generate output
		formatter := jsonformatter.NewJSONFormatter()
		
		// Get collector name from config
		collectorName := generalConfig.Operation["main"].Collector
		
		jsonResult, err := formatter.FormatResults(*folder_or_url, collectorName, messages, len(files), helpers.PDFTracker.Files)
		if err != nil {
			outputError("formatting_error", fmt.Sprintf("Error formatting JSON output: %v", err))
			return
		}
		
		// Generate HTML report
		htmlFormatter := htmlformatter.NewHTMLFormatter()
		
		if err := htmlFormatter.GenerateReport(jsonResult, *htmlOutput); err != nil {
			outputError("html_error", fmt.Sprintf("Error generating HTML report: %v", err))
			return
		}
		
		fmt.Printf("HTML report generated: %s\n", *htmlOutput)
	} else {
		// Regular scanning without TUI
		messages := utils.ApplyAllChecks(*generalConfig, files, true)
		
		// Get collector name from config
		collectorName := generalConfig.Operation["main"].Collector
		
		if *plainOutput {
			// Output plain text summary
			formatter := plainformatter.NewPlainFormatter()
			plainResult := formatter.FormatResults(*folder_or_url, collectorName, messages, len(files), helpers.PDFTracker.Files)
			fmt.Print(plainResult)
		} else {
			// Create JSON formatter and generate output (default behavior)
			formatter := jsonformatter.NewJSONFormatter()
			
			jsonResult, err := formatter.FormatResults(*folder_or_url, collectorName, messages, len(files), helpers.PDFTracker.Files)
			if err != nil {
				outputError("formatting_error", fmt.Sprintf("Error formatting JSON output: %v", err))
				return
			}
			
			// Output JSON
			fmt.Println(jsonResult)
		}
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
