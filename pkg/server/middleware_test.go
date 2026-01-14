package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractToken_Valid(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		token := GetTokenFromContext(r)
		if token != "test-token-123" {
			t.Errorf("Expected token 'test-token-123', got '%s'", token)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}
}

func TestExtractToken_MissingHeader(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called")
	})

	req := httptest.NewRequest("POST", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}
}

func TestExtractToken_InvalidFormat_NoBearer(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called")
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Basic test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}
}

func TestExtractToken_InvalidFormat_NoParts(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called")
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "BearerNoSpace")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}
}

func TestExtractToken_EmptyToken(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called")
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer ")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}
}

func TestExtractToken_CaseInsensitiveBearer(t *testing.T) {
	handler := ExtractToken(func(w http.ResponseWriter, r *http.Request) {
		token := GetTokenFromContext(r)
		if token != "my-token" {
			t.Errorf("Expected token 'my-token', got '%s'", token)
		}
		w.WriteHeader(http.StatusOK)
	})

	// Test lowercase "bearer"
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "bearer my-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200 for lowercase bearer, got %d", rr.Code)
	}

	// Test uppercase "BEARER"
	req2 := httptest.NewRequest("POST", "/test", nil)
	req2.Header.Set("Authorization", "BEARER my-token")
	rr2 := httptest.NewRecorder()

	handler.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Errorf("Expected status 200 for uppercase bearer, got %d", rr2.Code)
	}
}

func TestGetTokenFromContext_NoToken(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	token := GetTokenFromContext(req)
	if token != "" {
		t.Errorf("Expected empty string, got '%s'", token)
	}
}
