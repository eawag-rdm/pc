package main

import (
	"flag"
	"fmt"

	"github.com/eawag-rdm/pc/pkg/collectors"
	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/utils"
)

func main() {

	// implement small cli to call pc with config and a folder (both can have default args)
	// then the files will be collected with the local_collector and the checks will be applied
	// the results will be printed to the console
	// the exit code will be 0 if no errors were found, otherwise 1
	// the cli should have a help command to show the usage

	// Define default values for the config and folder arguments
	defaultConfig := "pc.toml"
	// current word directory
	defaultFolder := "."

	// Parse CLI arguments
	cfg := flag.String("config", defaultConfig, "Path to the config file")
	folder := flag.String("folder", defaultFolder, "Path to the folder")
	flag.Parse()
	// Check if help is requested
	help := flag.Bool("help", false, "Show usage information")
	flag.Parse()

	if *help {
		flag.Usage()
		return
	}
	// Print the received arguments for debugging
	fmt.Printf("Using config: %s\n", *cfg)
	fmt.Printf("Using folder: %s\n", *folder)

	files, err := collectors.LocalCollector(*folder, true)
	if err != nil {
		fmt.Printf("Error collecting files: %v\n", err)
		return
	}

	generalConfig := config.LoadConfig(*cfg)

	messages := utils.ApplyAllChecks(*generalConfig, files)
	for _, message := range messages {
		fmt.Println(message)
	}

}
