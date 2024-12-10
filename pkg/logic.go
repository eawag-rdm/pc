package main

const generalConfigFilePath = "../config.toml.example"
const collectorConfigFilePath = "../ckanCollectors.toml.example"

func main() {

	/* config, _ := config.LoadConfig(generalConfigFilePath)
	collectorConfig, _ := config.LoadCKANConfig(collectorConfigFilePath)
	files, err := collectors.CollectCkanFiles(collectorConfig)
	checks, err := collectors.CollectChecks()
	if err != nil {
		fmt.Println("Error collecting checks:", err)
		return
	}
	messages := utils.ApplyChecksFiltered(config, checks, files)

	for _, message := range messages {
		fmt.Println(message)
	} */
}
