# Task 10: Rate Limiting

**Status:** `[ ]` Not started

**Dependencies:** Task 09

## Objective

Implement rate limiting middleware to protect the API from abuse.

## Deliverables

### 1. Update `internal/server/api/middleware.go`

Add rate limiting to the existing middleware file:

```go
package api

import (
    "net/http"
    "sync"
    "time"

    "golang.org/x/time/rate"
)

// RateLimitConfig holds rate limiting configuration
type RateLimitConfig struct {
    RequestsPerSecond float64       // e.g., 10.0
    BurstSize         int           // e.g., 20
    CleanupInterval   time.Duration // e.g., 10 * time.Minute
}

// RateLimiter implements per-client rate limiting
type RateLimiter struct {
    config   RateLimitConfig
    limiters map[string]*clientLimiter
    mu       sync.RWMutex
}

type clientLimiter struct {
    limiter  *rate.Limiter
    lastSeen time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter

// Start begins the cleanup goroutine
func (rl *RateLimiter) Start(ctx context.Context)

// Middleware returns the rate limiting middleware
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        clientIP := getClientIP(r)
        limiter := rl.getLimiter(clientIP)

        if !limiter.Allow() {
            w.Header().Set("Retry-After", "1")
            w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%.0f", rl.config.RequestsPerSecond))
            respondError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "Rate limit exceeded. Please retry later.")
            return
        }

        next.ServeHTTP(w, r)
    })
}

// getLimiter returns the rate limiter for a client, creating one if needed
func (rl *RateLimiter) getLimiter(clientIP string) *rate.Limiter

// cleanup removes old limiters for clients not seen recently
func (rl *RateLimiter) cleanup()

// getClientIP extracts the client IP from the request
// Respects X-Forwarded-For and X-Real-IP headers
func getClientIP(r *http.Request) string
```

### 2. Implementation Details

```go
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
    return &RateLimiter{
        config:   cfg,
        limiters: make(map[string]*clientLimiter),
    }
}

func (rl *RateLimiter) Start(ctx context.Context) {
    go func() {
        ticker := time.NewTicker(rl.config.CleanupInterval)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                rl.cleanup()
            }
        }
    }()
}

func (rl *RateLimiter) getLimiter(clientIP string) *rate.Limiter {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    if cl, exists := rl.limiters[clientIP]; exists {
        cl.lastSeen = time.Now()
        return cl.limiter
    }

    limiter := rate.NewLimiter(rate.Limit(rl.config.RequestsPerSecond), rl.config.BurstSize)
    rl.limiters[clientIP] = &clientLimiter{
        limiter:  limiter,
        lastSeen: time.Now(),
    }
    return limiter
}

func (rl *RateLimiter) cleanup() {
    rl.mu.Lock()
    defer rl.mu.Unlock()

    cutoff := time.Now().Add(-3 * rl.config.CleanupInterval)
    for ip, cl := range rl.limiters {
        if cl.lastSeen.Before(cutoff) {
            delete(rl.limiters, ip)
        }
    }
}

func getClientIP(r *http.Request) string {
    // Check X-Forwarded-For first (may contain multiple IPs)
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        // Take the first IP (original client)
        if idx := strings.Index(xff, ","); idx != -1 {
            return strings.TrimSpace(xff[:idx])
        }
        return strings.TrimSpace(xff)
    }

    // Check X-Real-IP
    if xri := r.Header.Get("X-Real-IP"); xri != "" {
        return xri
    }

    // Fall back to RemoteAddr
    ip, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil {
        return r.RemoteAddr
    }
    return ip
}
```

### 3. `internal/server/api/ratelimit_test.go`

```go
package api

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

func TestGetClientIP(t *testing.T) {
    tests := []struct {
        name       string
        remoteAddr string
        headers    map[string]string
        expected   string
    }{
        {
            name:       "RemoteAddr only",
            remoteAddr: "192.168.1.1:12345",
            expected:   "192.168.1.1",
        },
        {
            name:       "X-Forwarded-For single",
            remoteAddr: "10.0.0.1:12345",
            headers:    map[string]string{"X-Forwarded-For": "203.0.113.1"},
            expected:   "203.0.113.1",
        },
        {
            name:       "X-Forwarded-For multiple",
            remoteAddr: "10.0.0.1:12345",
            headers:    map[string]string{"X-Forwarded-For": "203.0.113.1, 70.41.3.18, 150.172.238.178"},
            expected:   "203.0.113.1",
        },
        {
            name:       "X-Real-IP",
            remoteAddr: "10.0.0.1:12345",
            headers:    map[string]string{"X-Real-IP": "203.0.113.1"},
            expected:   "203.0.113.1",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Create request and test
        })
    }
}

func TestRateLimiter_Allow(t *testing.T) {
    // Test requests within limit pass
}

func TestRateLimiter_Block(t *testing.T) {
    // Test requests exceeding limit are blocked
}

func TestRateLimiter_Burst(t *testing.T) {
    // Test burst allowance works
}

func TestRateLimiter_PerClient(t *testing.T) {
    // Test different clients have separate limits
}

func TestRateLimiter_Cleanup(t *testing.T) {
    // Test old limiters are cleaned up
}

func TestRateLimiter_Headers(t *testing.T) {
    // Test Retry-After header is set
    // Test X-RateLimit-Limit header is set
}
```

## Tests Required

- `TestGetClientIP` - IP extraction from various headers
- `TestRateLimiter_Allow` - Normal requests pass
- `TestRateLimiter_Block` - Excess requests blocked
- `TestRateLimiter_Burst` - Burst handling
- `TestRateLimiter_PerClient` - Per-client isolation
- `TestRateLimiter_Cleanup` - Memory cleanup
- `TestRateLimiter_Headers` - Response headers

## Dependencies

Add to `go.mod`:
```
golang.org/x/time v0.5.0
```

## Acceptance Criteria

- [ ] Rate limiting respects configured limits
- [ ] Each client has separate rate limit
- [ ] Burst allowance works correctly
- [ ] Blocked requests return 429 with Retry-After header
- [ ] Client IP is extracted correctly from headers
- [ ] Old limiters are cleaned up periodically
- [ ] All tests pass
