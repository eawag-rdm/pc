package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime/pprof"
	"time"

	"github.com/eawag-rdm/pc/internal/analysis"
	"github.com/eawag-rdm/pc/internal/cpucap"
	"github.com/eawag-rdm/pc/pkg/checks"
	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
	"github.com/eawag-rdm/pc/pkg/metadata"
	"github.com/eawag-rdm/pc/pkg/output"
	htmlformatter "github.com/eawag-rdm/pc/pkg/output/html"
	jsonformatter "github.com/eawag-rdm/pc/pkg/output/json"
	plainformatter "github.com/eawag-rdm/pc/pkg/output/plain"
	"github.com/eawag-rdm/pc/pkg/output/tui"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

func main() {

	// Small CLI: collect files via the configured collector, apply the checks,
	// and render the results (TUI by default; -json/-plain/-html otherwise).
	// Errors are reported as JSON error envelopes on stdout.

	// Define default values for the config and folder arguments
	defaultConfig := config.FindConfigFile()
	// current word directory
	defaultFolder := "."

	// Parse CLI arguments
	cfg := flag.String("config", defaultConfig, "Path to the config file")
	folder_or_url := flag.String("location", defaultFolder, "Path to local folder or CKAN package name. It depends on the set collector.")
	help := flag.Bool("help", false, "Show usage information")
	noTui := flag.Bool("no-tui", false, "Disable interactive TUI viewer")
	jsonOutput := flag.Bool("json", false, "Output JSON format to stdout")
	htmlOutput := flag.String("html", "", "Generate HTML report to specified file (e.g., --html report.html)")
	plainOutput := flag.Bool("plain", false, "Output plain text summary to stdout")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile := flag.String("memprofile", "", "write memory profile to file")
	flag.Parse()

	// Validate mutually exclusive flags
	if *jsonOutput && *plainOutput {
		fmt.Fprintln(os.Stderr, "Error: --json and --plain cannot be used together. Please choose one output format.")
		os.Exit(1)
	}

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

	generalConfig, err := config.LoadConfig(*cfg)
	if err != nil {
		outputError("config_error", fmt.Sprintf("Error loading config: %v", err))
		return
	}

	// Pin the CPU budget before anything is sized off it. Nothing is printed:
	// every CLI stream is spoken for - stdout carries the JSON envelope (error
	// envelopes too, whatever the output flags say) and the tests read the
	// streams merged - so a startup notice here would break that contract. The
	// server, which has a logging channel of its own, reports its budget at boot.
	cpucap.Apply(generalConfig.General.EffectiveMaxCores())

	// The one boot gate for the rules (like the server's at boot): bind every
	// check's parameters and compile every include/exclude pattern once, here,
	// so a bad pattern, a wrong-typed parameter or an unknown section is an
	// operator error before anything scans - not a silently unfiltered (or
	// fully filtered) run.
	plan, err := utils.Compile(generalConfig, checks.NewRegistry())
	if err != nil {
		outputError("config_error", fmt.Sprintf("Invalid config: %v", err))
		return
	}

	var (
		files          []structs.File
		filesErr       error
		metadataResult *metadata.Metadata
	)

	// A missing [operation.main] section leaves a nil *OperationConfig in the
	// map; dereferencing .Collector would panic. Fail cleanly instead.
	op, ok := generalConfig.Operation["main"]
	if !ok || op == nil {
		outputError("collector_error", "No [operation.main] collector configured in the config file.")
		return
	}

	// Decide which collector to use
	if op.Collector == "LocalCollector" {
		files, filesErr = collectors.LocalCollector(*folder_or_url, *generalConfig)
		if filesErr != nil {
			outputError("collector_error", filesErr.Error())
			return
		}

	} else if op.Collector == "CkanCollector" {
		if *folder_or_url == "." {
			outputError("collector_error", "Please provide a CKAN package name (use the location flag '-location')")
			return
		}
		// Single package_show call; files and metadata both derive from it. The
		// CLI has no deadline of its own, so the call runs under Background.
		result, err := collectors.CkanPackageShow(context.Background(), *folder_or_url, *generalConfig)
		if err != nil {
			outputError("collector_error", err.Error())
			return
		}
		files, filesErr = collectors.CkanFilesFromResult(result, *generalConfig)
		if filesErr != nil {
			outputError("collector_error", filesErr.Error())
			return
		}
		metadataResult = metadata.CkanMetadataFromJSON(result)

	} else {
		outputError("collector_error", "Unknown collector")
		return
	}

	// Zero files is NOT an error: the analysis proceeds and the result carries a
	// clear "no files to analyse" notice (added by ApplyAllChecks), surfaced the
	// same way a skipped file is. This matches the server, which returns a normal
	// result for a package with no analyzable resources.

	// Determine output modes
	generateHtml := *htmlOutput != ""
	showTui := !*noTui && !*jsonOutput && !*plainOutput

	if showTui {
		// TUI mode (default behavior)
		app := tui.NewScanningApp()
		app.SetLocation(*folder_or_url)
		app.SetSummaryIntroText(generalConfig.General.SummaryIntroText)
		app.SetSummaryMaxIssuesBeforeTruncation(generalConfig.General.SummaryMaxIssuesBeforeTruncation)
		app.SetSummaryMinGroupSizeForTruncation(generalConfig.General.SummaryMinGroupSizeForTruncation)

		// Channel for scan completion
		scanComplete := make(chan *tui.ScanResult)
		scanErrors := make(chan error)

		// Store JSON result for potential HTML generation
		var jsonResultForHtml string

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
				res := analysis.Run(context.Background(), *generalConfig, plan, files, metadataResult, func(current, total int, message string) {
					app.UpdateProgress(current, total, message)
				})

				// Create JSON formatter and generate output
				formatter := jsonformatter.NewJSONFormatter()

				// Get collector name from config
				collectorName := generalConfig.Operation["main"].Collector

				jsonResult, err := formatter.FormatResults(*folder_or_url, collectorName, res.Messages, len(files), helpers.PDFTracker.SnapshotFiles(), res.Diagnostics)
				if err != nil {
					scanErrors <- fmt.Errorf("formatting error: %v", err)
					return
				}

				// Store for HTML generation if needed
				jsonResultForHtml = jsonResult

				// Generate HTML if requested (during TUI scan)
				if generateHtml {
					htmlFormatter := htmlformatter.NewHTMLFormatter()
					if err := htmlFormatter.GenerateReport(jsonResult, *htmlOutput); err != nil {
						scanErrors <- fmt.Errorf("HTML generation error: %v", err)
						return
					}
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

		// After TUI exits, print HTML generation message if applicable
		if generateHtml && jsonResultForHtml != "" {
			fmt.Printf("HTML report generated: %s\n", *htmlOutput)
		}
	} else {
		// Non-TUI mode: run regular scan
		res := analysis.Run(context.Background(), *generalConfig, plan, files, metadataResult, nil)

		// Get collector name from config
		collectorName := generalConfig.Operation["main"].Collector

		// Generate JSON result (needed for HTML and JSON output)
		formatter := jsonformatter.NewJSONFormatter()
		jsonResult, err := formatter.FormatResults(*folder_or_url, collectorName, res.Messages, len(files), helpers.PDFTracker.SnapshotFiles(), res.Diagnostics)
		if err != nil {
			outputError("formatting_error", fmt.Sprintf("Error formatting output: %v", err))
			return
		}

		// Generate HTML if requested
		if generateHtml {
			htmlFormatter := htmlformatter.NewHTMLFormatter()
			if err := htmlFormatter.GenerateReport(jsonResult, *htmlOutput); err != nil {
				outputError("html_error", fmt.Sprintf("Error generating HTML report: %v", err))
				return
			}
			fmt.Printf("HTML report generated: %s\n", *htmlOutput)
		}

		// Output to stdout based on flags
		if *jsonOutput {
			fmt.Println(jsonResult)
		} else if *plainOutput {
			plainFormatter := plainformatter.NewPlainFormatter()
			plainResult := plainFormatter.FormatResults(*folder_or_url, collectorName, res.Messages, len(files), helpers.PDFTracker.SnapshotFiles())
			fmt.Print(plainResult)
		}
		// If only --no-tui (with or without --html), no stdout output beyond HTML message
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
