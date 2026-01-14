package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eawag-rdm/pc/pkg/config"
)

func TestHandler_Health(t *testing.T) {
	handler := &Handler{
		pcConfig:  &config.Config{},
		serverCfg: Config{},
	}

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()

	handler.Health(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	var response HealthResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Status != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", response.Status)
	}
	if response.Version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got '%s'", response.Version)
	}
	if response.Timestamp == "" {
		t.Error("Expected non-empty timestamp")
	}
}

func TestHandler_Analyze_MissingPackageID(t *testing.T) {
	handler := &Handler{
		pcConfig:  &config.Config{},
		serverCfg: Config{},
	}

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req.Header.Set("Content-Type", "application/json")

	// Add token to context
	ctx := context.WithValue(req.Context(), CKANTokenKey, "test-token")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	handler.Analyze(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rr.Code)
	}

	var response ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Code != "missing_package_id" {
		t.Errorf("Expected code 'missing_package_id', got '%s'", response.Code)
	}
}

func TestHandler_Analyze_InvalidJSON(t *testing.T) {
	handler := &Handler{
		pcConfig:  &config.Config{},
		serverCfg: Config{},
	}

	body := bytes.NewBufferString(`{invalid json}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()

	handler.Analyze(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rr.Code)
	}

	var response ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Code != "invalid_json" {
		t.Errorf("Expected code 'invalid_json', got '%s'", response.Code)
	}
}

func TestHandler_Analyze_NoToken(t *testing.T) {
	handler := &Handler{
		pcConfig:  &config.Config{},
		serverCfg: Config{},
	}

	body := bytes.NewBufferString(`{"package_id": "test-package"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req.Header.Set("Content-Type", "application/json")
	// No token in context

	rr := httptest.NewRecorder()

	handler.Analyze(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}
}

func TestHandler_Analyze_NoCKANURL(t *testing.T) {
	handler := &Handler{
		pcConfig:  &config.Config{},
		serverCfg: Config{}, // No CKAN URL configured
	}

	body := bytes.NewBufferString(`{"package_id": "test-package"}`)
	req := httptest.NewRequest("POST", "/api/v1/analyze", body)
	req.Header.Set("Content-Type", "application/json")

	// Add token to context
	ctx := context.WithValue(req.Context(), CKANTokenKey, "test-token")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	handler.Analyze(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", rr.Code)
	}

	var response ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Code != "no_ckan_url" {
		t.Errorf("Expected code 'no_ckan_url', got '%s'", response.Code)
	}
}

func TestRespondJSON(t *testing.T) {
	rr := httptest.NewRecorder()

	data := map[string]string{"key": "value"}
	respondJSON(rr, http.StatusOK, data)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}

	var response map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["key"] != "value" {
		t.Errorf("Expected key='value', got '%s'", response["key"])
	}
}

func TestRespondError(t *testing.T) {
	rr := httptest.NewRecorder()

	respondError(rr, http.StatusBadRequest, "test_code", "Test error message")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", rr.Code)
	}

	var response ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Code != "test_code" {
		t.Errorf("Expected code 'test_code', got '%s'", response.Code)
	}
	if response.Error != "Test error message" {
		t.Errorf("Expected error 'Test error message', got '%s'", response.Error)
	}
}

func TestAnalyzeRequest_JSONParsing(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantID    string
		wantURL   string
	}{
		{
			name:    "basic package_id",
			input:   `{"package_id": "my-package"}`,
			wantID:  "my-package",
			wantURL: "",
		},
		{
			name:    "with ckan_url",
			input:   `{"package_id": "my-package", "ckan_url": "https://custom.ckan.org"}`,
			wantID:  "my-package",
			wantURL: "https://custom.ckan.org",
		},
		{
			name:    "empty ckan_url",
			input:   `{"package_id": "test", "ckan_url": ""}`,
			wantID:  "test",
			wantURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req AnalyzeRequest
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("Failed to parse JSON: %v", err)
			}

			if req.PackageID != tt.wantID {
				t.Errorf("PackageID = '%s', want '%s'", req.PackageID, tt.wantID)
			}
			if req.CkanURL != tt.wantURL {
				t.Errorf("CkanURL = '%s', want '%s'", req.CkanURL, tt.wantURL)
			}
		})
	}
}
