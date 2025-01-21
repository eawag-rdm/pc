package collectors

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/structs"
)

func Request(url, ckanToken string, verifyTLS bool) (string, error) {

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !verifyTLS,
			// If verifyTLS=false => InsecureSkipVerify=true
		},
	}

	client := &http.Client{
		Transport: transport,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	if ckanToken != "" {
		req.Header.Set("Authorization", ckanToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request failed with status code %d", resp.StatusCode)
	}

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
func GetCKANResources(jsonMap map[string]interface{}) ([]structs.File, error) {
	files := []structs.File{}
	if result, ok := jsonMap["result"].(map[string]interface{}); ok {
		if resources, ok := result["resources"].([]interface{}); ok {
			for _, resource := range resources {
				if res, ok := resource.(map[string]interface{}); ok {
					if resourceIsFile(res) {
						file := structs.ToFile(res["url"].(string), res["name"].(string), int64(res["size"].(float64)), "")
						files = append(files, file)
					}
				}
			}
		}
	}
	return files, nil
}

// getLocalResourcePath translates your Python logic to Go.
func getLocalResourcePath(resourceURL string, ckanStoragePath string) string {

	parsedURL, err := url.Parse(resourceURL)
	if err != nil {
		return ""
	}
	resourceID := strings.Split(parsedURL.Path, "/")[4]

	// Slice out parts: rsc_1, rsc_2, rsc_3
	// Make sure resourceID has at least 6 characters or handle errors as needed
	rsc1 := resourceID[:3]
	rsc2 := resourceID[3:6]
	rsc3 := resourceID[6:]

	localResourcePath := fmt.Sprintf("%s/%s/%s", rsc1, rsc2, rsc3)

	// If ckanStoragePath ends with "/", remove the slash
	ckanStoragePath = strings.TrimSuffix(ckanStoragePath, "/")

	// If ckanStoragePath is not empty, ensure it ends with "resources/"
	if ckanStoragePath != "" {
		if !strings.HasSuffix(ckanStoragePath, "resources") {
			ckanStoragePath += "/resources"
		}
		ckanStoragePath += "/"
	}

	return ckanStoragePath + localResourcePath
}

func CkanCollector(package_id string, config config.Config) ([]structs.File, error) {

	collectorName := "CkanCollector"

	urlAttr, ok := config.Collectors[collectorName].Attrs["url"].(string)
	if !ok {
		return nil, fmt.Errorf("url attribute not found or not a string")
	}

	url := fmt.Sprintf("%s/api/3/action/package_show?id=%s", urlAttr, package_id)
	token := config.Collectors[collectorName].Attrs["token"].(string)
	verify := config.Collectors[collectorName].Attrs["verify"].(bool)

	jsonStr, err := Request(url, token, verify)
	if err != nil {
		return nil, err
	}
	jsonMap, err := JSONToMap(jsonStr)
	if err != nil {
		return nil, err
	}

	files, err := GetCKANResources(jsonMap)
	if err != nil {
		return nil, err
	}

	localStoragePath := config.Collectors[collectorName].Attrs["ckan_storage_path"].(string)
	// Iterate files and apply getLocalResourcePath to each file to change the path in place
	for i, file := range files {
		files[i].Path = getLocalResourcePath(file.Path, localStoragePath)
	}

	return files, nil
}
