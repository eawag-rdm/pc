package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
)

// CKANAuthError represents an authentication/authorization error
type CKANAuthError struct {
	StatusCode int
	Message    string
}

func (e *CKANAuthError) Error() string {
	return e.Message
}

// VerifyCKANAccess checks if the provided token has read access to the specified package.
// It calls CKAN's package_show API and returns an error if access is denied.
func VerifyCKANAccess(ckanBaseURL, packageID, token string, verifyTLS bool) error {
	if ckanBaseURL == "" {
		return fmt.Errorf("CKAN base URL is not configured")
	}
	if packageID == "" {
		return fmt.Errorf("package ID is required")
	}
	if token == "" {
		return &CKANAuthError{
			StatusCode: http.StatusUnauthorized,
			Message:    "CKAN API token is required",
		}
	}

	// Build the package_show URL
	url := fmt.Sprintf("%s/api/3/action/package_show?id=%s", ckanBaseURL, packageID)

	// Create HTTP client with TLS configuration
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !verifyTLS,
		},
	}
	client := &http.Client{Transport: transport}

	// Create request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set authorization header
	req.Header.Set("Authorization", token)

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to verify CKAN access: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for error messages
	bodyBytes, _ := io.ReadAll(resp.Body)

	// Check response status
	switch resp.StatusCode {
	case http.StatusOK:
		// Access granted
		return nil
	case http.StatusUnauthorized:
		return &CKANAuthError{
			StatusCode: http.StatusUnauthorized,
			Message:    "Invalid or expired CKAN API token",
		}
	case http.StatusForbidden:
		return &CKANAuthError{
			StatusCode: http.StatusForbidden,
			Message:    fmt.Sprintf("Access denied to package '%s'", packageID),
		}
	case http.StatusNotFound:
		return &CKANAuthError{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("Package '%s' not found or not accessible", packageID),
		}
	default:
		return &CKANAuthError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("CKAN API error (status %d): %s", resp.StatusCode, string(bodyBytes)),
		}
	}
}

// IsCKANAuthError checks if an error is a CKANAuthError and returns the status code
func IsCKANAuthError(err error) (int, bool) {
	if authErr, ok := err.(*CKANAuthError); ok {
		return authErr.StatusCode, true
	}
	return 0, false
}
