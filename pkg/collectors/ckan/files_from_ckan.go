package collectors

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/eawag-rdm/pc/pkg/utils"
)

// send a get web request and return the json respo"github.com/eawag-rdm/pc/pkg/utils"nce; raise if return code is not 200
func Request(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Request failed with status code %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(bodyBytes), nil
}

// JSON string to map
func JSONToMap(jsonStr string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &result)
	return result, err
}

// Check if the resource is a file
func resourceIsFile(resource map[string]interface{}) bool {
	if url_type, ok := resource["url_type"].(string); ok {
		return url_type == "upload"
	}
	return false
}

// Expects parsed JSON and returns all resources of the CKAN package
func GetCKANResources(jsonMap map[string]interface{}) ([]utils.File, error) {
	files := []utils.File{}
	if result, ok := jsonMap["result"].(map[string]interface{}); ok {
		if resources, ok := result["resources"].([]interface{}); ok {
			for _, resource := range resources {
				if res, ok := resource.(map[string]interface{}); ok {
					if resourceIsFile(res) {
						file := utils.ToFile(res["url"].(string), res["name"].(string), int64(res["size"].(float64)), "")
						files = append(files, file)
					}
				}
			}
		}
	}
	return files, nil
}

func CollectCKANFiles(config CKANConfig) ([]utils.File, error) {
	url := fmt.Sprintf("%s/api/3/action/package_show?id=%s", config.CKANURL, config.PackageID)
	jsonStr, err := Request(url)
	if err != nil {
		return nil, err
	}
	jsonMap, err := JSONToMap(jsonStr)
	if err != nil {
		return nil, err
	}
	return GetCKANResources(jsonMap)
}

// CollectCKANFiles config struct
type CKANConfig struct {
	CKANURL   string
	PackageID string
}
