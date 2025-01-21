package collectors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
)

func TestJSONToMap(t *testing.T) {
	tests := []struct {
		name      string
		jsonStr   string
		want      map[string]interface{}
		expectErr bool
	}{
		{
			name:    "Valid JSON",
			jsonStr: `{"key1": "value1", "key2": 2}`,
			want: map[string]interface{}{
				"key1": "value1",
				"key2": float64(2),
			},
			expectErr: false,
		},
		{
			name:      "Invalid JSON",
			jsonStr:   `{"key1": "value1", "key2": 2`,
			want:      nil,
			expectErr: true,
		},
		{
			name:      "Empty JSON",
			jsonStr:   `{}`,
			want:      map[string]interface{}{},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := JSONToMap(tt.jsonStr)
			if (err != nil) != tt.expectErr {
				t.Errorf("JSONToMap() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("JSONToMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetCKANResources(t *testing.T) {
	// Read the test data file
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "test_ckan_metadata.json"))
	if err != nil {
		t.Fatalf("failed to read test data: %v", err)
	}

	// Parse the JSON data
	var jsonMap map[string]interface{}
	err = json.Unmarshal(data, &jsonMap)
	if err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	// Call the function to test
	files, err := GetCKANResources(jsonMap)
	if err != nil {
		t.Fatalf("GetCKANResources returned an error: %v", err)
	}

	// Check the results
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	expectedFile := structs.File{
		Path:   "https://opendata.eawag.ch/dataset/3c2ff3ab-151c-44a4-8769-fba684663020/resource/8bf5b5f2-75a0-4a6a-a484-8b4dacd324bc/download/finalreportlakeice.pdf",
		Name:   "finalreportlakeice.pdf",
		Size:   8655745,
		Suffix: ".pdf",
	}

	if files[0] != expectedFile {
		t.Errorf("expected file %+v, got %+v", expectedFile, files[0])
	}
}
func TestGetLocalResourcePath(t *testing.T) {
	tests := []struct {
		name            string
		resourceURL     string
		ckanStoragePath string
		expectedPath    string
		expectEmptyPath bool
	}{
		{
			name:            "Valid URL and storage path",
			resourceURL:     "https://opendata.eawag.ch/dataset/d4b2fee5-74f4-4513-8cd3-cfb957d84eb1/resource/f46e74be-1c61-4866-81da-9282c37c0c42/download/readme.md",
			ckanStoragePath: "/var/lib/ckan",
			expectedPath:    "/var/lib/ckan/resources/f46/e74/be-1c61-4866-81da-9282c37c0c42",
		},
		{
			name:            "URL with no storage path",
			resourceURL:     "https://opendata.eawag.ch/dataset/d4b2fee5-74f4-4513-8cd3-cfb957d84eb1/resource/f46e74be-1c61-4866-81da-9282c37c0c42/download/readme.md",
			ckanStoragePath: "",
			expectedPath:    "f46/e74/be-1c61-4866-81da-9282c37c0c42",
		},
		{
			name:            "Storage path with trailing slash",
			resourceURL:     "https://opendata.eawag.ch/dataset/d4b2fee5-74f4-4513-8cd3-cfb957d84eb1/resource/f46e74be-1c61-4866-81da-9282c37c0c42/download/readme.md",
			ckanStoragePath: "/var/lib/ckan/",
			expectedPath:    "/var/lib/ckan/resources/f46/e74/be-1c61-4866-81da-9282c37c0c42",
		},
		{
			name:            "Invalid URL",
			resourceURL:     "://invalid-url",
			ckanStoragePath: "/var/lib/ckan",
			expectEmptyPath: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getLocalResourcePath(tt.resourceURL, tt.ckanStoragePath)
			if tt.expectEmptyPath {
				if got != "" {
					t.Errorf("expected empty path, got %v", got)
				}
			} else {
				if got != tt.expectedPath {
					t.Errorf("expected %v, got %v", tt.expectedPath, got)
				}
			}
		})
	}
}
