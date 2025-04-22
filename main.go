package main

import (
	"flag"
	"fmt"

	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/helpers"
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
	flag.Parse()
	// Check if help is requested
	help := flag.Bool("help", false, "Show usage information")
	flag.Parse()

	if *help {
		flag.Usage()
		return
	}

	generalConfig := config.LoadConfig(*cfg)

	var (
		files []structs.File
		err   error
	)

	// Decide which collector to use
	if generalConfig.Operation["main"].Collector == "LocalCollector" {
		files, err = collectors.LocalCollector(*folder_or_url, *generalConfig)
		if err != nil {
			fmt.Printf("LocalCollector error collecting files: %v\n", err)
			return
		}

	} else if generalConfig.Operation["main"].Collector == "CkanCollector" {
		if *folder_or_url == "." {
			fmt.Println("Please provide a CKAN package name (use the location flag '-location')")
			return
		}
		files, err = collectors.CkanCollector(*folder_or_url, *generalConfig)
		if err != nil {
			fmt.Printf("CkanCollector error collecting files: %v\n", err)
			return
		}

	} else {
		fmt.Println("Unknown collector")
		return
	}

	messages := utils.ApplyAllChecks(*generalConfig, files, true)
	if len(messages) > 0 {
		fmt.Println("=== Results ===")
		for _, message := range messages {
			fmt.Println(message.Format())
		}
	}

	fmt.Println(helpers.PDFTracker.FormatFiles())
}
