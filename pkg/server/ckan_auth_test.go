package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyCKANAccess_Success(t *testing.T) {
	// Create mock CKAN server
	mockCKAN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authorization header
		auth := r.Header.Get("Authorization")
		if auth != "test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true, "result": {}}`))
	}))
	defer mockCKAN.Close()

	err := VerifyCKANAccess(mockCKAN.URL, "test-package", "test-token", false)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

func TestVerifyCKANAccess_Unauthorized(t *testing.T) {
	mockCKAN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockCKAN.Close()

	err := VerifyCKANAccess(mockCKAN.URL, "test-package", "bad-token", false)
	if err == nil {
		t.Error("Expected error, got nil")
	}

	if statusCode, isAuthErr := IsCKANAuthError(err); isAuthErr {
		if statusCode != http.StatusUnauthorized {
			t.Errorf("Expected status 401, got %d", statusCode)
		}
	} else {
		t.Error("Expected CKANAuthError")
	}
}

func TestVerifyCKANAccess_Forbidden(t *testing.T) {
	mockCKAN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer mockCKAN.Close()

	err := VerifyCKANAccess(mockCKAN.URL, "private-package", "valid-token", false)
	if err == nil {
		t.Error("Expected error, got nil")
	}

	if statusCode, isAuthErr := IsCKANAuthError(err); isAuthErr {
		if statusCode != http.StatusForbidden {
			t.Errorf("Expected status 403, got %d", statusCode)
		}
	} else {
		t.Error("Expected CKANAuthError")
	}
}

func TestVerifyCKANAccess_NotFound(t *testing.T) {
	mockCKAN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockCKAN.Close()

	err := VerifyCKANAccess(mockCKAN.URL, "nonexistent-package", "valid-token", false)
	if err == nil {
		t.Error("Expected error, got nil")
	}

	if statusCode, isAuthErr := IsCKANAuthError(err); isAuthErr {
		if statusCode != http.StatusNotFound {
			t.Errorf("Expected status 404, got %d", statusCode)
		}
	} else {
		t.Error("Expected CKANAuthError")
	}
}

func TestVerifyCKANAccess_EmptyURL(t *testing.T) {
	err := VerifyCKANAccess("", "test-package", "token", true)
	if err == nil {
		t.Error("Expected error for empty URL")
	}
}

func TestVerifyCKANAccess_EmptyPackageID(t *testing.T) {
	err := VerifyCKANAccess("https://ckan.example.com", "", "token", true)
	if err == nil {
		t.Error("Expected error for empty package ID")
	}
}

// TestVerifyCKANAccess_EmptyToken_PublicPath asserts the public-package path:
// an empty token is NOT short-circuited with a 401. It is forwarded to CKAN
// (here, an empty Authorization header), and a public package that CKAN serves
// 200 verifies successfully. Guarding on token=="" would 401 every anonymous
// request and the public path would never reach the collector (§2, §3).
func TestVerifyCKANAccess_EmptyToken_PublicPath(t *testing.T) {
	var gotAuth string
	var sawCall bool
	mockCKAN := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCall = true
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true, "result": {}}`))
	}))
	defer mockCKAN.Close()

	err := VerifyCKANAccess(mockCKAN.URL, "public-package", "", false)
	if err != nil {
		t.Fatalf("empty token on public package must not error, got %v", err)
	}
	if !sawCall {
		t.Error("expected the empty-token request to reach CKAN, but it short-circuited")
	}
	if gotAuth != "" {
		t.Errorf("expected empty Authorization header forwarded to CKAN, got %q", gotAuth)
	}
}

func TestCKANAuthError_Error(t *testing.T) {
	err := &CKANAuthError{
		StatusCode: 403,
		Message:    "Access denied",
	}

	if err.Error() != "Access denied" {
		t.Errorf("Expected 'Access denied', got '%s'", err.Error())
	}
}

func TestIsCKANAuthError(t *testing.T) {
	// Test with CKANAuthError
	authErr := &CKANAuthError{StatusCode: 401, Message: "test"}
	if statusCode, isAuth := IsCKANAuthError(authErr); !isAuth || statusCode != 401 {
		t.Errorf("Expected (401, true), got (%d, %v)", statusCode, isAuth)
	}

	// Test with regular error
	regularErr := http.ErrAbortHandler
	if _, isAuth := IsCKANAuthError(regularErr); isAuth {
		t.Error("Expected false for regular error")
	}

	// Test with nil
	if _, isAuth := IsCKANAuthError(nil); isAuth {
		t.Error("Expected false for nil")
	}
}
