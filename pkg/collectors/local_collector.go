package collectors

import (
	"os"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// read all files from a local directory
func LocalCollector(path string, config config.Config) ([]structs.File, error) {

	collectorName := "LocalCollector"

	foundFiles := []structs.File{}
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if !file.IsDir() {
			info, err := file.Info()
			if err != nil {
				return nil, err
			}
			foundFiles = append(foundFiles, structs.ToFile(path+"/"+file.Name(), file.Name(), info.Size(), ""))
		} else {
			if config.Collectors[collectorName].Attrs["includeFolders"] == "true" {
				foundFiles = append(foundFiles, structs.ToFile(path+"/"+file.Name(), file.Name(), -1, ""))
			}
		}
	}

	return foundFiles, nil
}
