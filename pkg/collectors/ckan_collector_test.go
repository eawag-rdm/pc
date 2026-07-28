package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

	_, reqErr := Request(context.Background(), reqURL, secretToken, true)

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
	_, _ = Request(context.Background(), reqURL, secretToken, true)
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
			_, err := CkanCollector(context.Background(), "some-package", cfg)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrContains)
			}
		})
	}
}

// TestCkanCollectorMissingSectionNoPanic asserts that a config whose Collectors
// map has NO "CkanCollector" entry (the [collector.CkanCollector] section is
// absent) yields a clean error instead of panicking on a nil-map dereference.
func TestCkanCollectorMissingSectionNoPanic(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
	}{
		{
			name: "nil Collectors map",
			cfg:  config.Config{},
		},
		{
			name: "Collectors map without CkanCollector entry",
			cfg: config.Config{
				Collectors: map[string]*config.CollectorConfig{},
			},
		},
		{
			name: "CkanCollector entry is a nil pointer",
			cfg: config.Config{
				Collectors: map[string]*config.CollectorConfig{
					"CkanCollector": nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// CkanCollector must return an error, not panic.
			_, err := CkanCollector(context.Background(), "pkg", tt.cfg)
			if err == nil {
				t.Fatalf("expected an error for a missing CkanCollector section, got nil")
			}
			if !strings.Contains(err.Error(), "CkanCollector configuration is missing") {
				t.Errorf("error %q does not mention the missing configuration", err.Error())
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
			got, err := getLocalResourcePath(tt.resourceURL, tt.ckanStoragePath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
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

// TestContainWithinRoot covers the path-containment primitive (spec §5):
// normal paths under the root are accepted; paths that escape via "../" or an
// absolute injection are rejected.
func TestContainWithinRoot(t *testing.T) {
	const root = "/srv/ckan/storage/resources/"

	tests := []struct {
		name     string
		resolved string
		wantOK   bool
	}{
		{
			name:     "normal sharded path under root",
			resolved: "/srv/ckan/storage/resources/f46/e74/be-1c61-4866-81da-9282c37c0c42",
			wantOK:   true,
		},
		{
			name:     "root itself",
			resolved: "/srv/ckan/storage/resources",
			wantOK:   true,
		},
		{
			name:     "escape via ../ above the root",
			resolved: "/srv/ckan/storage/resources/f46/../../../../../../etc/shadow",
			wantOK:   false,
		},
		{
			name:     "absolute injection outside the root",
			resolved: "/etc/shadow",
			wantOK:   false,
		},
		{
			name:     "sibling-prefix path is not contained",
			resolved: "/srv/ckan/storage/resources-evil/x",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := containWithinRoot(root, tt.resolved)
			if ok != tt.wantOK {
				t.Errorf("containWithinRoot(%q, %q) = %v, want %v", root, tt.resolved, ok, tt.wantOK)
			}
		})
	}
}

// TestGetLocalResourcePathContainment covers the containment guard as wired into
// getLocalResourcePath (spec §5). A normal resource id resolves to a cleaned
// path under the storage root. The escape-rejection path of the guard is proven
// directly in TestContainWithinRoot; here we confirm getLocalResourcePath
// returns the cleaned, contained path (and surfaces an ErrResourceUnreadable
// error if the guard ever trips).
func TestGetLocalResourcePathContainment(t *testing.T) {
	const root = "/srv/ckan/storage"

	url := "https://opendata.eawag.ch/dataset/d/resource/f46e74be-1c61-4866-81da-9282c37c0c42/download/readme.md"
	got, err := getLocalResourcePath(url, root)
	if err != nil {
		if errors.Is(err, ErrResourceUnreadable) {
			t.Fatalf("normal path was wrongly rejected as escaping: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Clean(root + "/resources/f46/e74/be-1c61-4866-81da-9282c37c0c42")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
	if !strings.HasPrefix(got, filepath.Clean(root+"/resources")+string(os.PathSeparator)) {
		t.Errorf("resolved path %q is not under the storage root", got)
	}
}

// TestResolveLocalResourceMissingOnDisk covers the missing-on-disk case (spec
// §5): an upload resource whose resolved file does not exist must yield an
// error wrapping ErrResourceUnreadable (so the server maps it to
// resource_unreadable) rather than being silently skipped. A present file
// resolves cleanly.
func TestResolveLocalResourceMissingOnDisk(t *testing.T) {
	root := t.TempDir()

	// Build a resource URL whose sharded id maps under root.
	// id "f46e74be-..." => resources/f46/e74/be-...
	const resID = "f46e74be-1c61-4866-81da-9282c37c0c42"
	url := "https://opendata.eawag.ch/dataset/d/resource/" + resID + "/download/readme.md"

	t.Run("missing file yields resource_unreadable sentinel", func(t *testing.T) {
		got, err := resolveLocalResource(url, "readme.md", root)
		if err == nil {
			t.Fatalf("expected an error for a missing file, got nil (path=%q)", got)
		}
		if !errors.Is(err, ErrResourceUnreadable) {
			t.Errorf("expected error wrapping ErrResourceUnreadable, got %v", err)
		}
		if !strings.Contains(err.Error(), "readme.md") {
			t.Errorf("error %q does not name the resource", err.Error())
		}
	})

	t.Run("present file resolves cleanly", func(t *testing.T) {
		dir := filepath.Join(root, "resources", "f46", "e74")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create dirs: %v", err)
		}
		fpath := filepath.Join(dir, "be-1c61-4866-81da-9282c37c0c42")
		if err := os.WriteFile(fpath, []byte("hello"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		got, err := resolveLocalResource(url, "readme.md", root)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != filepath.Clean(fpath) {
			t.Errorf("expected resolved path %q, got %q", filepath.Clean(fpath), got)
		}
	})
}

// TestRequestReturnsCKANError asserts the single package_show call surfaces a
// structured *CKANError carrying the effective HTTP status, including the
// HTTP-200-with-success:false case which CKAN uses for authorization/not-found
// (spec §3, §5). The error message never leaks the token or the URL.
func TestRequestReturnsCKANError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantBody   bool // status derived from a success:false body
	}{
		{"http 401", http.StatusUnauthorized, ``, http.StatusUnauthorized, false},
		{"http 403", http.StatusForbidden, ``, http.StatusForbidden, false},
		{"http 404", http.StatusNotFound, ``, http.StatusNotFound, false},
		{"http 500", http.StatusInternalServerError, ``, http.StatusInternalServerError, false},
		{
			name:       "200 with success:false authorization error -> 404",
			status:     http.StatusOK,
			body:       `{"success":false,"error":{"__type":"Authorization Error","message":"Access denied"}}`,
			wantStatus: http.StatusNotFound,
			wantBody:   true,
		},
		{
			name:       "200 with success:false not found error -> 404",
			status:     http.StatusOK,
			body:       `{"success":false,"error":{"__type":"Not Found Error"}}`,
			wantStatus: http.StatusNotFound,
			wantBody:   true,
		},
		{
			// An unrecognised __type on a 200+success:false body resolves to 500
			// with StatusFromBody set; the server maps this StatusFromBody-500 to
			// internal_error (CKAN was reachable), distinct from a transport 5xx.
			name:       "200 with success:false unknown type -> 500 from body",
			status:     http.StatusOK,
			body:       `{"success":false,"error":{"__type":"Validation Error","message":"secret-detail"}}`,
			wantStatus: http.StatusInternalServerError,
			wantBody:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				if tt.body != "" {
					io.WriteString(w, tt.body)
				}
			}))
			defer srv.Close()

			_, err := Request(context.Background(), srv.URL+"/api/3/action/package_show?id=secret-pkg", "secret-token", false)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			var ckanErr *CKANError
			if !errors.As(err, &ckanErr) {
				t.Fatalf("expected *CKANError, got %T (%v)", err, err)
			}
			if ckanErr.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", ckanErr.StatusCode, tt.wantStatus)
			}
			if ckanErr.StatusFromBody != tt.wantBody {
				t.Errorf("StatusFromBody = %v, want %v", ckanErr.StatusFromBody, tt.wantBody)
			}
			if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "secret-pkg") {
				t.Errorf("CKANError message leaked a secret/URL: %q", err.Error())
			}
		})
	}
}

// TestRequestTransportError asserts a connection failure surfaces a transport
// *CKANError (mapped to ckan_unavailable by the server), with no secret leak.
func TestRequestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/api/3/action/package_show?id=secret-pkg"
	srv.Close() // close so the connection is refused

	_, err := Request(context.Background(), url, "secret-token", false)
	if err == nil {
		t.Fatalf("expected a transport error, got nil")
	}
	var ckanErr *CKANError
	if !errors.As(err, &ckanErr) {
		t.Fatalf("expected *CKANError, got %T (%v)", err, err)
	}
	if !ckanErr.Transport {
		t.Errorf("expected Transport=true, got %+v", ckanErr)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "secret-pkg") {
		t.Errorf("transport CKANError leaked a secret/URL: %q", err.Error())
	}
}

// TestRequestBodyReadFailure asserts that a connection dying mid-body (HTTP 200
// received, but the response body cut short) surfaces a transport *CKANError —
// the same class as a failed dial — rather than a raw read error the server
// would map to internal_error.
func TestRequestBodyReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Declare more bytes than are written; the handler returning early makes
		// the server abort the connection so the client read fails mid-body.
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"succ`)
	}))
	defer srv.Close()

	_, err := Request(context.Background(), srv.URL+"/api/3/action/package_show?id=secret-pkg", "secret-token", false)
	if err == nil {
		t.Fatalf("expected a transport error, got nil")
	}
	var ckanErr *CKANError
	if !errors.As(err, &ckanErr) {
		t.Fatalf("expected *CKANError, got %T (%v)", err, err)
	}
	if !ckanErr.Transport {
		t.Errorf("expected Transport=true, got %+v", ckanErr)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "secret-pkg") {
		t.Errorf("transport CKANError leaked a secret/URL: %q", err.Error())
	}
}

// TestRequestOversizedBody asserts that a response body exceeding
// maxCKANResponseBytes is rejected as a transport *CKANError instead of being
// read into memory unbounded.
func TestRequestOversizedBody(t *testing.T) {
	saved := maxCKANResponseBytes
	maxCKANResponseBytes = 1024
	defer func() { maxCKANResponseBytes = saved }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(make([]byte, 2048))
	}))
	defer srv.Close()

	_, err := Request(context.Background(), srv.URL+"/api/3/action/package_show?id=secret-pkg", "secret-token", false)
	if err == nil {
		t.Fatalf("expected a transport error, got nil")
	}
	var ckanErr *CKANError
	if !errors.As(err, &ckanErr) {
		t.Fatalf("expected *CKANError, got %T (%v)", err, err)
	}
	if !ckanErr.Transport {
		t.Errorf("expected Transport=true, got %+v", ckanErr)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "secret-pkg") {
		t.Errorf("transport CKANError leaked a secret/URL: %q", err.Error())
	}
}

// TestRequestContextDeadlineAborts asserts that an expired/cancelled context
// aborts the in-flight CKAN call instead of blocking on a hung socket: against a
// server that never responds, Request returns promptly (well before any
// server-side WriteTimeout) with a transport *CKANError (spec §2). Without
// http.NewRequestWithContext this test would hang.
func TestRequestContextDeadlineAborts(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never respond until released
	}))
	// Defers run LIFO: close(block) must release the parked handler BEFORE
	// srv.Close() waits on it, or Close() blocks forever.
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Request(ctx, srv.URL+"/api/3/action/package_show?id=hang-pkg", "secret-token", false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected a transport error from the cancelled context, got nil")
	}
	var ckanErr *CKANError
	if !errors.As(err, &ckanErr) || !ckanErr.Transport {
		t.Fatalf("expected a transport *CKANError, got %T (%v)", err, err)
	}
	// The deadline is 100ms; allow generous slack but prove it did not hang.
	if elapsed > 5*time.Second {
		t.Errorf("Request did not honor the context deadline: took %v", elapsed)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "hang-pkg") {
		t.Errorf("error leaked a secret/URL: %q", err.Error())
	}
}

// TestRequestEscapesPackageID asserts CkanCollector url.QueryEscapes the package
// id when building the package_show URL (spec §2): a crafted id arrives at CKAN
// escaped, not as raw query syntax.
func TestRequestEscapesPackageID(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"resources":[]}}`)
	}))
	defer srv.Close()

	cfg := config.Config{
		Collectors: map[string]*config.CollectorConfig{
			"CkanCollector": {Attrs: map[string]interface{}{
				"url":               srv.URL,
				"token":             "",
				"verify":            false,
				"ckan_storage_path": "",
			}},
		},
	}
	// A space must be percent-encoded by QueryEscape (+), proving escaping ran.
	_, _ = CkanCollector(context.Background(), "a b", cfg)
	if !strings.Contains(gotRawQuery, "id=a+b") {
		t.Errorf("expected escaped query id=a+b, got raw query %q", gotRawQuery)
	}
}
