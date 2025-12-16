# Task 11: Server Configuration

**Status:** `[ ]` Not started

**Dependencies:** Task 10

## Objective

Implement server configuration loading and the main server setup.

## Deliverables

### 1. `internal/server/config.go`

```go
package server

import (
    "os"
    "time"

    "github.com/BurntSushi/toml"
)

// Config holds all server configuration
type Config struct {
    Host string `toml:"host"`
    Port int    `toml:"port"`

    // Path to pc.toml for the analyzer
    PCConfigPath string `toml:"pc_config_path"`

    // Database
    DatabasePath string `toml:"database_path"`

    // Rate limiting
    RateLimit RateLimitConfig `toml:"rate_limit"`

    // Cache settings
    Cache CacheConfig `toml:"cache"`

    // Worker settings
    Worker WorkerConfig `toml:"worker"`
}

type RateLimitConfig struct {
    RequestsPerSecond float64 `toml:"requests_per_second"`
    BurstSize         int     `toml:"burst_size"`
}

type CacheConfig struct {
    TTL             Duration `toml:"ttl"`
    CleanupInterval Duration `toml:"cleanup_interval"`
}

type WorkerConfig struct {
    MaxConcurrent int      `toml:"max_concurrent"`
    QueueSize     int      `toml:"queue_size"`
    JobRetention  Duration `toml:"job_retention"`
}

// Duration is a wrapper for time.Duration that supports TOML parsing
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
    parsed, err := time.ParseDuration(string(text))
    if err != nil {
        return err
    }
    *d = Duration(parsed)
    return nil
}

func (d Duration) Duration() time.Duration {
    return time.Duration(d)
}

// DefaultConfig returns configuration with sensible defaults
func DefaultConfig() *Config {
    return &Config{
        Host:         "localhost",
        Port:         8080,
        PCConfigPath: "./pc.toml",
        DatabasePath: "./pc-server.db",
        RateLimit: RateLimitConfig{
            RequestsPerSecond: 10.0,
            BurstSize:         20,
        },
        Cache: CacheConfig{
            TTL:             Duration(24 * time.Hour),
            CleanupInterval: Duration(1 * time.Hour),
        },
        Worker: WorkerConfig{
            MaxConcurrent: 4,
            QueueSize:     100,
            JobRetention:  Duration(7 * 24 * time.Hour),
        },
    }
}

// LoadConfig loads configuration from a TOML file
// Missing values are filled with defaults
func LoadConfig(path string) (*Config, error) {
    cfg := DefaultConfig()

    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            // Use defaults if config file doesn't exist
            return cfg, nil
        }
        return nil, err
    }

    if err := toml.Unmarshal(data, cfg); err != nil {
        return nil, err
    }

    return cfg, nil
}

// Validate checks the configuration for errors
func (c *Config) Validate() error {
    // Check port range
    // Check paths exist
    // Check positive values
}
```

### 2. `internal/server/server.go`

```go
package server

import (
    "context"
    "fmt"
    "log"
    "net/http"
    "time"

    "github.com/eawag-rdm/pc/internal/server/analyzer"
    "github.com/eawag-rdm/pc/internal/server/api"
    "github.com/eawag-rdm/pc/internal/server/cache"
    "github.com/eawag-rdm/pc/internal/server/store"
    "github.com/eawag-rdm/pc/internal/server/worker"
)

// Version is set at build time
var Version = "dev"

// Server is the main HTTP server
type Server struct {
    config     *Config
    httpServer *http.Server
    db         *store.DB
    worker     *worker.Worker
    rateLimiter *api.RateLimiter
}

// New creates a new server with all dependencies
func New(cfg *Config) (*Server, error) {
    // 1. Initialize database
    db, err := store.New(cfg.DatabasePath)
    if err != nil {
        return nil, fmt.Errorf("database init failed: %w", err)
    }

    if err := db.Migrate(context.Background()); err != nil {
        return nil, fmt.Errorf("database migration failed: %w", err)
    }

    // 2. Initialize stores
    jobStore := store.NewJobStore(db.Conn())
    tokenStore := store.NewTokenStore(db.Conn())
    cacheStore := cache.NewSQLiteCache(db.Conn())

    // 3. Initialize analyzer
    anlzr, err := analyzer.New(cfg.PCConfigPath)
    if err != nil {
        return nil, fmt.Errorf("analyzer init failed: %w", err)
    }

    // 4. Initialize worker
    wrk := worker.New(jobStore, cacheStore, anlzr, worker.Config{
        MaxWorkers: cfg.Worker.MaxConcurrent,
        QueueSize:  cfg.Worker.QueueSize,
        CacheTTL:   cfg.Cache.TTL.Duration(),
    })

    // 5. Initialize rate limiter
    rateLimiter := api.NewRateLimiter(api.RateLimitConfig{
        RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
        BurstSize:         cfg.RateLimit.BurstSize,
        CleanupInterval:   10 * time.Minute,
    })

    // 6. Initialize handlers
    handler := api.NewHandler(jobStore, cacheStore, wrk, Version)

    // 7. Initialize auth middleware
    authMiddleware := api.NewAuthMiddleware(tokenStore)
    authMiddleware.AddPublicPath("/health")

    // 8. Set up routes
    mux := http.NewServeMux()
    handler.RegisterRoutes(mux)

    // 9. Apply middleware chain: rate limit -> auth -> handler
    var httpHandler http.Handler = mux
    httpHandler = authMiddleware.Middleware(httpHandler)
    httpHandler = rateLimiter.Middleware(httpHandler)
    httpHandler = loggingMiddleware(httpHandler)

    // 10. Create HTTP server
    httpServer := &http.Server{
        Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
        Handler:      httpHandler,
        ReadTimeout:  30 * time.Second,
        WriteTimeout: 5 * time.Minute, // Analysis can take time
        IdleTimeout:  120 * time.Second,
    }

    return &Server{
        config:      cfg,
        httpServer:  httpServer,
        db:          db,
        worker:      wrk,
        rateLimiter: rateLimiter,
    }, nil
}

// Start starts the server
func (s *Server) Start(ctx context.Context) error {
    // Start background workers
    s.worker.Start()
    s.rateLimiter.Start(ctx)

    log.Printf("Starting PC server on %s", s.httpServer.Addr)
    return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
    log.Println("Shutting down server...")

    // Stop accepting new requests
    if err := s.httpServer.Shutdown(ctx); err != nil {
        return err
    }

    // Stop worker
    s.worker.Stop()

    // Close database
    return s.db.Close()
}

// loggingMiddleware logs all requests
func loggingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
    })
}
```

### 3. `internal/server/config_test.go`

```go
package server

import (
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestDefaultConfig(t *testing.T) {
    // Test defaults are sensible
}

func TestLoadConfig(t *testing.T) {
    // Test loading valid config
    // Test loading non-existent file (uses defaults)
    // Test loading invalid TOML
}

func TestConfig_Validate(t *testing.T) {
    // Test valid config
    // Test invalid port
    // Test invalid paths
}

func TestDuration_UnmarshalText(t *testing.T) {
    // Test various duration formats
    // Test invalid format
}
```

### 4. Example Configuration File

Create `pc-server.toml.example`:

```toml
# PC Server Configuration

# Server address
host = "0.0.0.0"
port = 8080

# Path to the PC analyzer configuration
pc_config_path = "/etc/pc/pc.toml"

# SQLite database path
database_path = "/var/lib/pc-server/pc-server.db"

# Rate limiting
[rate_limit]
requests_per_second = 10.0
burst_size = 20

# Cache settings
[cache]
ttl = "24h"
cleanup_interval = "1h"

# Worker settings
[worker]
max_concurrent = 4
queue_size = 100
job_retention = "168h"  # 7 days
```

## Tests Required

- `TestDefaultConfig` - Default values
- `TestLoadConfig` - Config file loading
- `TestConfig_Validate` - Validation logic
- `TestDuration_UnmarshalText` - Duration parsing

## Acceptance Criteria

- [ ] Config loads from TOML file
- [ ] Missing config uses defaults
- [ ] Invalid config returns clear error
- [ ] Duration parsing works with various formats
- [ ] Server initializes all components
- [ ] Middleware chain is correctly ordered
- [ ] Example config file is provided
- [ ] All tests pass
