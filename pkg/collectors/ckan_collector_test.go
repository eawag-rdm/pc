package collectors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
	"github.com/eawag-rdm/pc/pkg/output"
	"github.com/eawag-rdm/pc/pkg/structs"
)

// buildCKANJSON builds a minimal package_show-shaped jsonMap from a package
// name and a set of resources, for unit-testing GetCKANResources.
func buildCKANJSON(pkgName string, resources []map[string]interface{}) map[string]interface{} {
	resList := make([]interface{}, len(resources))
	for i, r := range resources {
		resList[i] = r
	}
	return map[string]interface{}{
		"result": map[string]interface{}{
			"name":      pkgName,
			"resources": resList,
		},
	}
}

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
		Path:        "https://opendata.eawag.ch/dataset/3c2ff3ab-151c-44a4-8769-fba684663020/resource/8bf5b5f2-75a0-4a6a-a484-8b4dacd324bc/download/finalreportlakeice.pdf",
		Name:        "finalreportlakeice.pdf",
		DisplayName: "finalreportlakeice.pdf",
		Size:        8655745,
		Suffix:      ".pdf",
	}

	if files[0] != expectedFile {
		t.Errorf("expected file %+v, got %+v", expectedFile, files[0])
	}
}

func TestGetCKANResourcesMalformedAndSkips(t *testing.T) {
	const pkg = "my-package"

	tests := []struct {
		name            string
		resource        map[string]interface{}
		wantErr         bool
		wantErrContains []string
		wantFiles       int
		wantFileName    string
	}{
		{
			name:            "both null",
			resource:        map[string]interface{}{"name": "res-a", "url_type": nil, "url": nil},
			wantErr:         true,
			wantErrContains: []string{"res-a", pkg},
		},
		{
			name:            "both empty string",
			resource:        map[string]interface{}{"name": "res-b", "url_type": "", "url": ""},
			wantErr:         true,
			wantErrContains: []string{"res-b", pkg},
		},
		{
			name:            "url_type null, url empty string",
			resource:        map[string]interface{}{"name": "res-c", "url_type": nil, "url": ""},
			wantErr:         true,
			wantErrContains: []string{"res-c", pkg},
		},
		{
			name:            "url_type empty string, url null",
			resource:        map[string]interface{}{"name": "res-d", "url_type": "", "url": nil},
			wantErr:         true,
			wantErrContains: []string{"res-d", pkg},
		},
		{
			name:            "both keys absent",
			resource:        map[string]interface{}{"name": "res-e"},
			wantErr:         true,
			wantErrContains: []string{"res-e", pkg},
		},
		{
			name:            "name absent falls back to id in message",
			resource:        map[string]interface{}{"id": "res-id-xyz", "url_type": nil, "url": nil},
			wantErr:         true,
			wantErrContains: []string{"res-id-xyz", pkg},
		},
		{
			name:      "external link (url_type null, url present) is skipped",
			resource:  map[string]interface{}{"name": "res-doi", "url_type": nil, "url": "https://doi.org/10.25678/0000nn"},
			wantErr:   false,
			wantFiles: 0,
		},
		{
			name: "valid upload is processed",
			resource: map[string]interface{}{
				"name":     "file.pdf",
				"url_type": "upload",
				"url":      "https://opendata.eawag.ch/dataset/d/resource/f46e74be-1c61-4866-81da-9282c37c0c42/download/file.pdf",
				"size":     float64(123),
			},
			wantErr:      false,
			wantFiles:    1,
			wantFileName: "file.pdf",
		},
		{
			name: "upload missing size returns clean error (no panic)",
			resource: map[string]interface{}{
				"name":     "broken.pdf",
				"url_type": "upload",
				"url":      "https://opendata.eawag.ch/dataset/d/resource/abcdef12-1c61-4866-81da-9282c37c0c42/download/broken.pdf",
				"size":     nil,
			},
			wantErr:         true,
			wantErrContains: []string{"broken.pdf", pkg, "missing required"},
		},
		{
			name: "upload missing url returns clean error (no panic)",
			resource: map[string]interface{}{
				"name":     "nourl.pdf",
				"url_type": "upload",
				"url":      nil,
				"size":     float64(123),
			},
			wantErr:         true,
			wantErrContains: []string{"nourl.pdf", pkg, "missing required"},
		},
		{
			name: "upload missing name returns clean error (no panic)",
			resource: map[string]interface{}{
				"name":     nil,
				"url_type": "upload",
				"url":      "https://opendata.eawag.ch/dataset/d/resource/abcdef12-1c61-4866-81da-9282c37c0c42/download/x.pdf",
				"size":     float64(123),
			},
			wantErr:         true,
			wantErrContains: []string{pkg, "missing required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonMap := buildCKANJSON(pkg, []map[string]interface{}{tt.resource})
			files, err := GetCKANResources(jsonMap)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				for _, sub := range tt.wantErrContains {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q does not contain %q", err.Error(), sub)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(files) != tt.wantFiles {
				t.Fatalf("expected %d files, got %d", tt.wantFiles, len(files))
			}
			if tt.wantFileName != "" && files[0].Name != tt.wantFileName {
				t.Errorf("expected file name %q, got %q", tt.wantFileName, files[0].Name)
			}
		})
	}
}

// TestRequestNeverLeaksToken is a regression test for the deleted token print
// at ckan_collector.go:46. On a non-200 response the collector must not emit the
// token (or the query-carrying URL) to any output stream.
func TestRequestNeverLeaksToken(t *testing.T) {
	const secretToken = "super-secret-ckan-token-DO-NOT-LEAK"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a private package: CKAN denies access.
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	reqURL := srv.URL + "/api/3/action/package_show?id=secret-package"

	// Capture both stdout (non-JSON GlobalLogger path) and the JSON message
	// buffer to be sure the token cannot leak through either channel.
	output.GlobalLogger.SetJSONMode(false)
	defer output.GlobalLogger.SetJSONMode(false)

	origStdout := os.Stdout
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = wPipe

	_, reqErr := Request(reqURL, secretToken, true)

	wPipe.Close()
	os.Stdout = origStdout

	captured, _ := io.ReadAll(rPipe)
	stdout := string(captured)

	if reqErr == nil {
		t.Fatalf("expected an error from non-200 response, got nil")
	}
	if strings.Contains(stdout, secretToken) {
		t.Errorf("stdout leaked the token: %q", stdout)
	}
	if strings.Contains(reqErr.Error(), secretToken) {
		t.Errorf("error message leaked the token: %q", reqErr.Error())
	}
	if strings.Contains(stdout, "secret-package") || strings.Contains(reqErr.Error(), "secret-package") {
		t.Errorf("output leaked the query-carrying URL: stdout=%q err=%q", stdout, reqErr.Error())
	}

	// Repeat with the JSON-mode buffer to cover the structured-message path.
	output.GlobalLogger.SetJSONMode(true)
	output.GlobalLogger.ClearMessages()
	_, _ = Request(reqURL, secretToken, true)
	for _, m := range output.GlobalLogger.GetMessages() {
		if strings.Contains(m.Message, secretToken) || strings.Contains(m.Message, "secret-package") {
			t.Errorf("JSON log message leaked a secret/URL: %q", m.Message)
		}
	}
}

// TestCkanCollectorMissingAttrsNoPanic asserts the collector returns clean
// errors (instead of panicking) when required collector attributes are absent
// or of the wrong type.
func TestCkanCollectorMissingAttrsNoPanic(t *testing.T) {
	tests := []struct {
		name            string
		attrs           map[string]interface{}
		wantErrContains string
	}{
		{
			name:            "url missing",
			attrs:           map[string]interface{}{},
			wantErrContains: "url attribute",
		},
		{
			name:            "token missing",
			attrs:           map[string]interface{}{"url": "http://example.invalid"},
			wantErrContains: "token attribute",
		},
		{
			name:            "verify missing",
			attrs:           map[string]interface{}{"url": "http://example.invalid", "token": ""},
			wantErrContains: "verify attribute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Collectors: map[string]*config.CollectorConfig{
					"CkanCollector": {Attrs: tt.attrs},
				},
			}
			_, err := CkanCollector("some-package", cfg)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrContains)
			}
		})
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
