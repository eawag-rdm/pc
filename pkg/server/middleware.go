package server

import (
	"context"
	"net/http"
	"strings"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	// CKANTokenKey is the context key for the CKAN API token
	CKANTokenKey contextKey = "ckan_token"
)

// ExtractToken extracts the Bearer token from the Authorization header
// and stores it in the request context
func ExtractToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			respondError(w, http.StatusUnauthorized, "missing_token", "Authorization header required")
			return
		}

		// Parse Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			respondError(w, http.StatusUnauthorized, "invalid_auth_format", "Expected 'Authorization: Bearer <token>'")
			return
		}

		token := strings.TrimSpace(parts[1])
		if token == "" {
			respondError(w, http.StatusUnauthorized, "empty_token", "Token cannot be empty")
			return
		}

		// Store token in context and proceed
		ctx := context.WithValue(r.Context(), CKANTokenKey, token)
		next(w, r.WithContext(ctx))
	}
}

// GetTokenFromContext retrieves the CKAN token from the request context
func GetTokenFromContext(r *http.Request) string {
	if token, ok := r.Context().Value(CKANTokenKey).(string); ok {
		return token
	}
	return ""
}

// LoggingMiddleware logs incoming requests
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simple logging - could be enhanced with timestamps, response time, etc.
		// For now, keeping it minimal as the user requested simple implementation
		next.ServeHTTP(w, r)
	})
}
