package pkg

import (
	"fmt"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/eawag-rdm/pc/pkg/utils"
)

func MainLogic(generalConfigFilePath string, fileCollector func(config.Config) ([]structs.File, error)) []structs.Message {

	generalConfig := config.LoadConfig(generalConfigFilePath)
	files, err := fileCollector(generalConfig)
	if err != nil {
		fmt.Println("Error collecting files:", err)
		return nil
	}

	return utils.ApplyAllChecks(generalConfig, files)

}
