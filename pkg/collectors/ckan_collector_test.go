package collectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	neturl "net/url"
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

// ckanTestConfig builds a minimal CkanCollector config pointing at srvURL.
func ckanTestConfig(srvURL string) config.Config {
	return config.Config{Collectors: map[string]*config.CollectorConfig{
		"CkanCollector": {Attrs: map[string]interface{}{
			"url": srvURL, "token": "", "verify": false, "ckan_storage_path": "",
		}},
	}}
}

// TestCkanPackageShow_MissingConfig asserts a missing or nil
// [collector.CkanCollector] section yields a clean error, not a nil-map panic.
func TestCkanPackageShow_MissingConfig(t *testing.T) {
	_, err := CkanPackageShow(context.Background(), "some-pkg", config.Config{
		Collectors: map[string]*config.CollectorConfig{},
	})
	if err == nil {
		t.Fatal("expected an error for missing CkanCollector config, got nil")
	}
	_, err = CkanPackageShow(context.Background(), "some-pkg", config.Config{
		Collectors: map[string]*config.CollectorConfig{"CkanCollector": nil},
	})
	if err == nil {
		t.Fatal("expected an error for a nil CkanCollector config, got nil")
	}
}

// TestCkanPackageShow_StatusError asserts a non-200 CKAN response surfaces a
// typed *CKANStatusError carrying the upstream status.
func TestCkanPackageShow_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := CkanPackageShow(context.Background(), "nope", ckanTestConfig(srv.URL))
	var statusErr *CKANError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected *CKANError, got %T (%v)", err, err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}
}

// TestCkanPackageShow_BadBody asserts unusable 200 bodies (non-JSON, or JSON
// without a result object) yield clean errors.
func TestCkanPackageShow_BadBody(t *testing.T) {
	for name, body := range map[string]string{
		"not json":  "<html>oops</html>",
		"no result": `{"success": true}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, body)
			}))
			defer srv.Close()
			if _, err := CkanPackageShow(context.Background(), "some-pkg", ckanTestConfig(srv.URL)); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

// TestCkanPackageShow_SuccessAndEscaping asserts the happy path returns the
// result object and query-escapes the package id.
func TestCkanPackageShow_SuccessAndEscaping(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		io.WriteString(w, `{"result":{"name":"my-pkg"}}`)
	}))
	defer srv.Close()

	result, err := CkanPackageShow(context.Background(), "a&evil=1", ckanTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["name"] != "my-pkg" {
		t.Errorf("result not returned, got %v", result)
	}
	if gotQuery != "id=a%26evil%3D1" {
		t.Errorf("package id not escaped: got query %q", gotQuery)
	}
}

// probeTestConfig builds the probe's CkanCollector config with a token, so a
// test can assert the token travels with the search request.
func probeTestConfig(srvURL, token string) config.Config {
	cfg := ckanTestConfig(srvURL)
	cfg.Collectors["CkanCollector"].Attrs["token"] = token
	return cfg
}

// TestCkanPackageModifiedAt_SuccessAndEscaping asserts the probe returns the
// searched package's metadata_modified and builds the query CKAN needs: both id
// spellings quoted inside fq, one row, private packages included - with the
// package id escaped, so an id carrying query syntax cannot restructure the
// request. The request carries the caller's token, which is what makes a
// private package visible to its owner and invisible to everybody else.
func TestCkanPackageModifiedAt_SuccessAndEscaping(t *testing.T) {
	var gotQuery neturl.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"success":true,"result":{"count":1,"results":[`+
			`{"id":"a&evil=1","metadata_modified":"2026-08-26T09:00:00.000000"}]}}`)
	}))
	defer srv.Close()

	modified, ok, err := CkanPackageModifiedAt(context.Background(), "a&evil=1", probeTestConfig(srv.URL, "probe-token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("a single exact match must be usable")
	}
	if modified != "2026-08-26T09:00:00.000000" {
		t.Errorf("metadata_modified = %q, want the searched package's value", modified)
	}
	if gotAuth != "probe-token" {
		t.Errorf("Authorization = %q, want the configured token - CKAN decides visibility by it", gotAuth)
	}
	// Decoded: an unescaped id would have split fq at its '&' and lost the
	// second half into a parameter of its own.
	if want := `(name:"a&evil=1" OR id:"a&evil=1")`; gotQuery.Get("fq") != want {
		t.Errorf("fq = %q, want %q", gotQuery.Get("fq"), want)
	}
	if got := gotQuery.Get("rows"); got != "1" {
		t.Errorf("rows = %q, want 1", got)
	}
	// Without it, stock CKAN answers no private dataset at all and the cache is
	// inert for every token-gated package.
	if got := gotQuery.Get("include_private"); got != "true" {
		t.Errorf("include_private = %q, want true", got)
	}
	// fl would hand back raw Solr documents, whose metadata_modified is
	// serialized differently from package_show's - no cached entry would ever
	// match again.
	if gotQuery.Has("fl") {
		t.Errorf("fl must not be sent, got %q", gotQuery.Get("fl"))
	}
	if got := gotQuery.Get("evil"); got != "" {
		t.Errorf("the package id leaked into its own parameter: evil=%q", got)
	}
}

// TestCkanPackageModifiedAt_SolrTermEscaping asserts the quoted Solr terms
// survive an id carrying the two characters that would otherwise end or reshape
// the phrase. URL escaping does not cover this: CKAN decodes the parameter
// before Solr parses it, so an embedded quote would close the term and turn the
// rest of the id into query syntax.
func TestCkanPackageModifiedAt_SolrTermEscaping(t *testing.T) {
	var gotFq string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFq = r.URL.Query().Get("fq")
		io.WriteString(w, `{"success":true,"result":{"count":0,"results":[]}}`)
	}))
	defer srv.Close()

	if _, _, err := CkanPackageModifiedAt(context.Background(), `a" OR name:*`, ckanTestConfig(srv.URL)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `(name:"a\" OR name:*" OR id:"a\" OR name:*")`; gotFq != want {
		t.Errorf("fq = %q, want %q", gotFq, want)
	}
}

// TestCkanPackageModifiedAt_UnusableResults asserts every ambiguous answer is a
// plain "cannot say" - no error, no value - so the caller falls back to
// package_show instead of acting on it. A count of 0 in particular must not be
// read as "absent": an unauthorized package looks exactly the same.
func TestCkanPackageModifiedAt_UnusableResults(t *testing.T) {
	for name, body := range map[string]string{
		"no match":               `{"success":true,"result":{"count":0,"results":[]}}`,
		"several matches":        `{"success":true,"result":{"count":2,"results":[{"id":"pkg","metadata_modified":"2026-08-26T09:00:00.000000"}]}}`,
		"missing field":          `{"success":true,"result":{"count":1,"results":[{"id":"pkg"}]}}`,
		"empty field":            `{"success":true,"result":{"count":1,"results":[{"id":"pkg","metadata_modified":""}]}}`,
		"count without a result": `{"success":true,"result":{"count":1,"results":[]}}`,
		"result is not a dict":   `{"success":true,"result":{"count":1,"results":["x"]}}`,
		// A hit on a neighbouring package would be cached as this one's
		// freshness: the filter query is trusted for nothing.
		"another package": `{"success":true,"result":{"count":1,"results":[{"id":"other","name":"other","metadata_modified":"2026-08-26T09:00:00.000000"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, body)
			}))
			defer srv.Close()

			modified, ok, err := CkanPackageModifiedAt(context.Background(), "pkg", ckanTestConfig(srv.URL))
			if err != nil {
				t.Errorf("an ambiguous search is not an error, got %v", err)
			}
			if ok {
				t.Errorf("ok = true for %q, want false", body)
			}
			if modified != "" {
				t.Errorf("modified = %q, want empty when unusable", modified)
			}
		})
	}
}

// TestCkanPackageModifiedAt_Failures asserts a probe that could not be made
// returns the typed error for logging - never a usable value.
func TestCkanPackageModifiedAt_Failures(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, ok, err := CkanPackageModifiedAt(context.Background(), "pkg", ckanTestConfig(srv.URL))
		if ok {
			t.Error("a failed probe must not be usable")
		}
		var ckanErr *CKANError
		if !errors.As(err, &ckanErr) {
			t.Fatalf("expected *CKANError, got %T (%v)", err, err)
		}
		if ckanErr.StatusCode != http.StatusInternalServerError {
			t.Errorf("StatusCode = %d, want 500", ckanErr.StatusCode)
		}
	})

	t.Run("action failure in a 200 body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"success":false,"error":{"__type":"Validation Error"}}`)
		}))
		defer srv.Close()

		_, ok, err := CkanPackageModifiedAt(context.Background(), "pkg", ckanTestConfig(srv.URL))
		if ok {
			t.Error("a failed probe must not be usable")
		}
		if err == nil {
			t.Fatal("expected an error for success:false")
		}
	})

	// The Detail is what the server logs, so it must name the call that
	// actually failed - not package_show, which was never made.
	t.Run("unparseable body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html>oops</html>")
		}))
		defer srv.Close()

		_, ok, err := CkanPackageModifiedAt(context.Background(), "pkg", ckanTestConfig(srv.URL))
		if ok {
			t.Error("a failed probe must not be usable")
		}
		var ckanErr *CKANError
		if !errors.As(err, &ckanErr) {
			t.Fatalf("expected *CKANError, got %T (%v)", err, err)
		}
		if ckanErr.Detail != unusableDetailSearchMalformedJSON {
			t.Errorf("Detail = %q, want %q", ckanErr.Detail, unusableDetailSearchMalformedJSON)
		}
	})

	t.Run("body without a result object", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"success":true}`)
		}))
		defer srv.Close()

		_, ok, err := CkanPackageModifiedAt(context.Background(), "pkg", ckanTestConfig(srv.URL))
		if ok {
			t.Error("a failed probe must not be usable")
		}
		var ckanErr *CKANError
		if !errors.As(err, &ckanErr) {
			t.Fatalf("expected *CKANError, got %T (%v)", err, err)
		}
		if ckanErr.Detail != unusableDetailSearchNoResult {
			t.Errorf("Detail = %q, want %q", ckanErr.Detail, unusableDetailSearchNoResult)
		}
	})

	t.Run("missing config", func(t *testing.T) {
		_, ok, err := CkanPackageModifiedAt(context.Background(), "pkg", config.Config{
			Collectors: map[string]*config.CollectorConfig{"CkanCollector": nil},
		})
		if ok || err == nil {
			t.Fatalf("expected an error for a nil CkanCollector config, got ok=%v err=%v", ok, err)
		}
	})
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
		RelPath:     "finalreportlakeice.pdf",
		DisplayName: "finalreportlakeice.pdf",
		Size:        8655745,
		Suffix:      ".pdf",
	}

	if files[0] != expectedFile {
		t.Errorf("expected file %+v, got %+v", expectedFile, files[0])
	}
}

// TestCkanCollectorRelPath asserts a resource's RelPath is its CKAN name
// (the namespace is flat) and that it survives the FileStore rewrite, which
// replaces Path with the local blob path. A resource with an empty name falls
// back to the URL basename, for RelPath exactly as for Name.
func TestCkanCollectorRelPath(t *testing.T) {
	root := t.TempDir()

	// blobFor creates the FileStore blob a resource id shards to.
	blobFor := func(t *testing.T, resID string) string {
		t.Helper()
		dir := filepath.Join(root, "resources", resID[:3], resID[3:6])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create dirs: %v", err)
		}
		blob := filepath.Join(dir, resID[6:])
		if err := os.WriteFile(blob, []byte("hello"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}
		return blob
	}
	resURL := func(resID, filename string) string {
		return "https://opendata.eawag.ch/dataset/d/resource/" + resID + "/download/" + filename
	}

	const namedID = "f46e74be-1c61-4866-81da-9282c37c0c42"
	const unnamedID = "a1b2c3d4-1c61-4866-81da-9282c37c0c42"
	namedBlob := blobFor(t, namedID)
	unnamedBlob := blobFor(t, unnamedID)

	jsonMap := buildCKANJSON("my-package", []map[string]interface{}{
		{"url_type": "upload", "name": "data file.csv", "url": resURL(namedID, "data%20file.csv"), "size": float64(5)},
		{"url_type": "upload", "name": "", "url": resURL(unnamedID, "blob.csv"), "size": float64(5)},
	})
	result := jsonMap["result"].(map[string]interface{})

	cfg := config.Config{Collectors: map[string]*config.CollectorConfig{
		"CkanCollector": {Attrs: map[string]interface{}{"ckan_storage_path": root}},
	}}

	files, err := CkanFilesFromResult(result, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	tests := []struct {
		name     string
		file     structs.File
		wantPath string
		wantRel  string
	}{
		{"named resource", files[0], namedBlob, "data file.csv"},
		{"empty resource name falls back to the URL base", files[1], unnamedBlob, "blob.csv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.file.Path != tt.wantPath {
				t.Errorf("Path = %q, want the FileStore blob path %q", tt.file.Path, tt.wantPath)
			}
			if tt.file.RelPath != tt.wantRel {
				t.Errorf("RelPath = %q, want %q", tt.file.RelPath, tt.wantRel)
			}
			if tt.file.RelPath != tt.file.Name {
				t.Errorf("RelPath = %q, want it to equal Name %q", tt.file.RelPath, tt.file.Name)
			}
		})
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
// received, but the response body cut short) surfaces a transport *CKANError -
// the same class as a failed dial - rather than a raw read error the server
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
// maxCKANResponseBytes is rejected as an UNUSABLE *CKANError instead of being
// read into memory unbounded. It is deliberately not a transport error: a
// response did arrive (nothing failed at the socket level), it is simply
// unusable - the server maps both to ckan_unavailable but logs them apart.
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
		t.Fatalf("expected an error, got nil")
	}
	var ckanErr *CKANError
	if !errors.As(err, &ckanErr) {
		t.Fatalf("expected *CKANError, got %T (%v)", err, err)
	}
	if !ckanErr.Unusable {
		t.Errorf("expected Unusable=true, got %+v", ckanErr)
	}
	if ckanErr.Transport {
		t.Errorf("an oversized body is not a transport failure, got %+v", ckanErr)
	}
	if ckanErr.Detail == "" {
		t.Error("expected a Detail diagnostic for the CLI, got none")
	}
	if !strings.Contains(err.Error(), ckanErr.Detail) {
		t.Errorf("Error() must carry the Detail; got %q, want it to contain %q", err.Error(), ckanErr.Detail)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "secret-pkg") {
		t.Errorf("unusable CKANError leaked a secret/URL: %q", err.Error())
	}
}

// TestCkanPackageShowUnusableBody asserts a CKAN 200 whose body cannot be used -
// malformed JSON, or valid JSON with no "result" object - surfaces a structured
// *CKANError{Unusable:true} rather than a bare fmt.Errorf. This is what lets the
// server classify it as an upstream condition (ckan_unavailable, no admin
// alert). Error() must carry the safe Detail (the CLI prints it and has no
// server log to fall back on) and must leak neither the token nor the URL.
func TestCkanPackageShowUnusableBody(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantDetail string
	}{
		{"malformed JSON", `{"success":true,"result":{`, "malformed JSON in package_show response"},
		{"no result object", `{"success":true}`, "package_show response has no 'result' object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			cfg := config.Config{
				Collectors: map[string]*config.CollectorConfig{
					"CkanCollector": {Attrs: map[string]interface{}{
						"url":               srv.URL,
						"token":             "secret-token",
						"verify":            false,
						"ckan_storage_path": "",
					}},
				},
			}

			_, err := CkanPackageShow(context.Background(), "secret-pkg", cfg)
			if err == nil {
				t.Fatalf("expected an error for an unusable body, got nil")
			}
			var ckanErr *CKANError
			if !errors.As(err, &ckanErr) {
				t.Fatalf("expected *CKANError, got %T (%v)", err, err)
			}
			if !ckanErr.Unusable {
				t.Errorf("expected Unusable=true, got %+v", ckanErr)
			}
			if ckanErr.Transport {
				t.Errorf("an unusable body is not a transport failure, got %+v", ckanErr)
			}
			if ckanErr.StatusCode != 0 {
				t.Errorf("expected no status code on an unusable body, got %d", ckanErr.StatusCode)
			}
			if ckanErr.Detail != tt.wantDetail {
				t.Errorf("Detail = %q, want %q", ckanErr.Detail, tt.wantDetail)
			}
			if !strings.Contains(err.Error(), tt.wantDetail) {
				t.Errorf("Error() = %q, want it to carry the detail %q", err.Error(), tt.wantDetail)
			}
			if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "secret-pkg") ||
				strings.Contains(err.Error(), srv.URL) {
				t.Errorf("unusable CKANError leaked a secret/URL: %q", err.Error())
			}
		})
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

// TestRequestReusesConnection asserts consecutive CKAN calls share one client and
// therefore reuse the pooled connection instead of dialing (and handshaking)
// again per request.
func TestRequestReusesConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"resources":[]}}`)
	}))
	defer srv.Close()

	url := srv.URL + "/api/3/action/package_show?id=pkg"
	doRequest := func() bool {
		var reused bool
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		})
		if _, err := Request(ctx, url, "", false); err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		return reused
	}

	if doRequest() {
		t.Errorf("first request unexpectedly reused a connection")
	}
	if !doRequest() {
		t.Errorf("second request dialed a new connection; the transport is not shared")
	}
}

// TestRequestVerifyTLSSelectsVerifyingClient pins the client selection in
// Request: against a self-signed server, verifyTLS=true must fail certificate
// verification while verifyTLS=false must succeed. An inverted selection would
// silently disable verification in production.
func TestRequestVerifyTLSSelectsVerifyingClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":{"resources":[]}}`)
	}))
	defer srv.Close()

	url := srv.URL + "/api/3/action/package_show?id=pkg"

	_, err := Request(context.Background(), url, "", true)
	var ckanErr *CKANError
	if !errors.As(err, &ckanErr) || !ckanErr.Transport {
		t.Fatalf("verifyTLS=true against a self-signed cert: want transport CKANError, got %v", err)
	}

	if _, err := Request(context.Background(), url, "", false); err != nil {
		t.Fatalf("verifyTLS=false against a self-signed cert: want success, got %v", err)
	}
}
