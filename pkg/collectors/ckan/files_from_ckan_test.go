package collectors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/utils"
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
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "ckan_metadata.json"))
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

	expectedFile := utils.File{
		Path:   "https://opendata.eawag.ch/dataset/3c2ff3ab-151c-44a4-8769-fba684663020/resource/8bf5b5f2-75a0-4a6a-a484-8b4dacd324bc/download/finalreportlakeice.pdf",
		Name:   "finalreportlakeice.pdf",
		Size:   8655745,
		Suffix: ".pdf",
	}

	if files[0] != expectedFile {
		t.Errorf("expected file %+v, got %+v", expectedFile, files[0])
	}
}
