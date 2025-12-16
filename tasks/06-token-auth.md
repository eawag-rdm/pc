# Task 06: Token Store and Auth Middleware

**Status:** `[ ]` Not started

**Dependencies:** Task 03

## Objective

Implement token storage and Bearer token authentication middleware.

## Deliverables

### 1. `internal/server/store/tokens.go`

```go
package store

import (
    "context"
    "crypto/rand"
    "crypto/sha256"
    "database/sql"
    "encoding/hex"
    "errors"
    "time"

    "github.com/eawag-rdm/pc/internal/server/models"
)

var (
    ErrTokenNotFound = errors.New("token not found")
    ErrTokenExpired  = errors.New("token expired")
    ErrTokenInactive = errors.New("token inactive")
)

// TokenStore manages API tokens
type TokenStore struct {
    db *sql.DB
}

// NewTokenStore creates a new token store
func NewTokenStore(db *sql.DB) *TokenStore

// Create generates a new token and stores its hash
// Returns the plain token (only returned once, never stored)
func (s *TokenStore) Create(ctx context.Context, name string, expiresAt *time.Time) (string, error)

// Validate checks if a token is valid
// Returns nil if valid, appropriate error otherwise
func (s *TokenStore) Validate(ctx context.Context, plainToken string) error

// Revoke deactivates a token by name
func (s *TokenStore) Revoke(ctx context.Context, name string) error

// List returns all tokens (without the actual token values)
func (s *TokenStore) List(ctx context.Context) ([]*models.APIToken, error)

// Delete permanently removes a token by name
func (s *TokenStore) Delete(ctx context.Context, name string) error

// GenerateToken creates a cryptographically secure random token
// Format: "pcs_" + 32 random hex chars = 36 chars total
func GenerateToken() (string, error)

// HashToken creates a SHA256 hash of the token for storage
func HashToken(token string) string
```

### 2. `internal/server/store/tokens_test.go`

```go
package store

import (
    "context"
    "testing"
    "time"
)

func TestGenerateToken(t *testing.T) {
    // Test token format (prefix + length)
    // Test uniqueness (generate multiple)
}

func TestHashToken(t *testing.T) {
    // Test hash is deterministic
    // Test different tokens have different hashes
}

func TestTokenStore_Create(t *testing.T) {
    // Test creating token
    // Test token is returned only once
    // Test with expiration
    // Test without expiration
}

func TestTokenStore_Validate(t *testing.T) {
    // Test valid token
    // Test invalid token
    // Test expired token
    // Test revoked token
}

func TestTokenStore_Revoke(t *testing.T) {
    // Test revoking existing token
    // Test revoking non-existent token
}

func TestTokenStore_List(t *testing.T) {
    // Test listing tokens
    // Test token values are not returned
}
```

### 3. `internal/server/api/middleware.go`

```go
package api

import (
    "context"
    "net/http"
    "strings"

    "github.com/eawag-rdm/pc/internal/server/store"
)

// contextKey is used for storing values in request context
type contextKey string

const (
    // ContextKeyTokenName stores the validated token name in context
    ContextKeyTokenName contextKey = "token_name"
)

// AuthMiddleware provides Bearer token authentication
type AuthMiddleware struct {
    tokens *store.TokenStore
    // Paths that don't require authentication
    publicPaths map[string]bool
}

// NewAuthMiddleware creates a new auth middleware
func NewAuthMiddleware(tokens *store.TokenStore) *AuthMiddleware

// AddPublicPath marks a path as not requiring authentication
func (m *AuthMiddleware) AddPublicPath(path string)

// Middleware returns the HTTP middleware handler
func (m *AuthMiddleware) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Check if path is public
        if m.publicPaths[r.URL.Path] {
            next.ServeHTTP(w, r)
            return
        }

        // Extract Bearer token
        auth := r.Header.Get("Authorization")
        if auth == "" {
            respondError(w, http.StatusUnauthorized, "unauthorized", "Missing Authorization header")
            return
        }

        if !strings.HasPrefix(auth, "Bearer ") {
            respondError(w, http.StatusUnauthorized, "unauthorized", "Invalid Authorization format")
            return
        }

        token := strings.TrimPrefix(auth, "Bearer ")

        // Validate token
        if err := m.tokens.Validate(r.Context(), token); err != nil {
            respondError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired token")
            return
        }

        next.ServeHTTP(w, r)
    })
}
```

### 4. `internal/server/api/middleware_test.go`

```go
package api

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestAuthMiddleware_PublicPath(t *testing.T) {
    // Test public paths don't require auth
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
    // Test missing Authorization header
}

func TestAuthMiddleware_InvalidFormat(t *testing.T) {
    // Test non-Bearer format
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
    // Test invalid token
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
    // Test valid token passes through
}

func TestAuthMiddleware_ExpiredToken(t *testing.T) {
    // Test expired token is rejected
}
```

## Tests Required

- Token generation and hashing tests
- Token CRUD operations tests
- Auth middleware tests for all scenarios

## Acceptance Criteria

- [ ] Tokens are generated securely with proper prefix
- [ ] Tokens are stored as hashes (never plain)
- [ ] Validation works correctly for all cases
- [ ] Middleware correctly blocks unauthenticated requests
- [ ] Public paths bypass authentication
- [ ] All tests pass
