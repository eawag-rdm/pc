package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// send a get web request and return the json responce; raise if return code is not 200
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
func GetCKANResources(jsonMap map[string]interface{}) ([]File, error) {
	files := []File{}
	if result, ok := jsonMap["result"].(map[string]interface{}); ok {
		if resources, ok := result["resources"].([]interface{}); ok {
			for _, resource := range resources {
				if res, ok := resource.(map[string]interface{}); ok {
					if resourceIsFile(res) {
						file := toFile(res["url"].(string), res["name"].(string), int64(res["size"].(float64)), "")
						files = append(files, file)
					}
				}
			}
		}
	}
	return files, nil
}
