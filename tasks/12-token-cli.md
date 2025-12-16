# Task 12: Token Management CLI

**Status:** `[ ]` Not started

**Dependencies:** Task 06, Task 11

## Objective

Add CLI commands for managing API tokens.

## Deliverables

### 1. Update `cmd/pc-server/main.go`

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "os"
    "os/signal"
    "syscall"
    "text/tabwriter"
    "time"

    "github.com/eawag-rdm/pc/internal/server"
    "github.com/eawag-rdm/pc/internal/server/store"
)

func main() {
    // Define subcommands
    if len(os.Args) < 2 {
        printUsage()
        os.Exit(1)
    }

    switch os.Args[1] {
    case "serve":
        cmdServe(os.Args[2:])
    case "token":
        if len(os.Args) < 3 {
            printTokenUsage()
            os.Exit(1)
        }
        switch os.Args[2] {
        case "create":
            cmdTokenCreate(os.Args[3:])
        case "list":
            cmdTokenList(os.Args[3:])
        case "revoke":
            cmdTokenRevoke(os.Args[3:])
        default:
            printTokenUsage()
            os.Exit(1)
        }
    case "help", "-h", "--help":
        printUsage()
    default:
        fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
        printUsage()
        os.Exit(1)
    }
}

func printUsage() {
    fmt.Println(`PC Server - Package Checker HTTP API

Usage:
    pc-server <command> [options]

Commands:
    serve       Start the HTTP server
    token       Manage API tokens
    help        Show this help message

Use "pc-server <command> -h" for more information about a command.`)
}

func printTokenUsage() {
    fmt.Println(`Manage API tokens

Usage:
    pc-server token <subcommand> [options]

Subcommands:
    create      Create a new API token
    list        List all tokens
    revoke      Revoke a token

Examples:
    pc-server token create --name "CKAN Production"
    pc-server token create --name "Test" --expires 720h
    pc-server token list
    pc-server token revoke --name "Test"`)
}
```

### 2. Serve Command

```go
func cmdServe(args []string) {
    fs := flag.NewFlagSet("serve", flag.ExitOnError)
    configPath := fs.String("config", "pc-server.toml", "Path to config file")
    fs.Parse(args)

    // Load config
    cfg, err := server.LoadConfig(*configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
        os.Exit(1)
    }

    // Create server
    srv, err := server.New(cfg)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error creating server: %v\n", err)
        os.Exit(1)
    }

    // Handle shutdown signals
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        <-sigCh
        fmt.Println("\nReceived shutdown signal...")
        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer shutdownCancel()
        srv.Shutdown(shutdownCtx)
        cancel()
    }()

    // Start server
    if err := srv.Start(ctx); err != nil && err != http.ErrServerClosed {
        fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
        os.Exit(1)
    }
}
```

### 3. Token Commands

```go
func cmdTokenCreate(args []string) {
    fs := flag.NewFlagSet("token create", flag.ExitOnError)
    configPath := fs.String("config", "pc-server.toml", "Path to config file")
    name := fs.String("name", "", "Token name/description (required)")
    expires := fs.String("expires", "", "Expiration duration (e.g., 720h)")
    fs.Parse(args)

    if *name == "" {
        fmt.Fprintln(os.Stderr, "Error: --name is required")
        fs.Usage()
        os.Exit(1)
    }

    cfg, err := server.LoadConfig(*configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
        os.Exit(1)
    }

    db, err := store.New(cfg.DatabasePath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
        os.Exit(1)
    }
    defer db.Close()

    tokenStore := store.NewTokenStore(db.Conn())

    var expiresAt *time.Time
    if *expires != "" {
        d, err := time.ParseDuration(*expires)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Invalid duration: %v\n", err)
            os.Exit(1)
        }
        t := time.Now().Add(d)
        expiresAt = &t
    }

    token, err := tokenStore.Create(context.Background(), *name, expiresAt)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error creating token: %v\n", err)
        os.Exit(1)
    }

    fmt.Println("Token created successfully!")
    fmt.Println("")
    fmt.Printf("Token: %s\n", token)
    fmt.Println("")
    fmt.Println("Store this token securely - it cannot be retrieved again.")
    if expiresAt != nil {
        fmt.Printf("Expires: %s\n", expiresAt.Format(time.RFC3339))
    } else {
        fmt.Println("Expires: never")
    }
}

func cmdTokenList(args []string) {
    fs := flag.NewFlagSet("token list", flag.ExitOnError)
    configPath := fs.String("config", "pc-server.toml", "Path to config file")
    fs.Parse(args)

    cfg, err := server.LoadConfig(*configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
        os.Exit(1)
    }

    db, err := store.New(cfg.DatabasePath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
        os.Exit(1)
    }
    defer db.Close()

    tokenStore := store.NewTokenStore(db.Conn())

    tokens, err := tokenStore.List(context.Background())
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error listing tokens: %v\n", err)
        os.Exit(1)
    }

    if len(tokens) == 0 {
        fmt.Println("No tokens found.")
        return
    }

    w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
    fmt.Fprintln(w, "NAME\tCREATED\tEXPIRES\tSTATUS")
    for _, t := range tokens {
        expires := "never"
        if t.ExpiresAt != nil {
            expires = t.ExpiresAt.Format("2006-01-02")
        }
        status := "active"
        if !t.Active {
            status = "revoked"
        }
        fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
            t.Name,
            t.CreatedAt.Format("2006-01-02"),
            expires,
            status,
        )
    }
    w.Flush()
}

func cmdTokenRevoke(args []string) {
    fs := flag.NewFlagSet("token revoke", flag.ExitOnError)
    configPath := fs.String("config", "pc-server.toml", "Path to config file")
    name := fs.String("name", "", "Token name to revoke (required)")
    fs.Parse(args)

    if *name == "" {
        fmt.Fprintln(os.Stderr, "Error: --name is required")
        fs.Usage()
        os.Exit(1)
    }

    cfg, err := server.LoadConfig(*configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
        os.Exit(1)
    }

    db, err := store.New(cfg.DatabasePath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
        os.Exit(1)
    }
    defer db.Close()

    tokenStore := store.NewTokenStore(db.Conn())

    if err := tokenStore.Revoke(context.Background(), *name); err != nil {
        fmt.Fprintf(os.Stderr, "Error revoking token: %v\n", err)
        os.Exit(1)
    }

    fmt.Printf("Token '%s' has been revoked.\n", *name)
}
```

## CLI Usage Examples

```bash
# Start server
pc-server serve
pc-server serve --config /etc/pc-server/config.toml

# Create tokens
pc-server token create --name "CKAN Production"
pc-server token create --name "Test" --expires 720h
pc-server token create --name "CI/CD" --config /etc/pc-server/config.toml

# List tokens
pc-server token list
pc-server token list --config /etc/pc-server/config.toml

# Revoke token
pc-server token revoke --name "Test"
```

## Tests Required

No automated tests for CLI (difficult to test). Manual testing checklist:

- [ ] `pc-server serve` starts the server
- [ ] `pc-server serve --config <path>` uses custom config
- [ ] `pc-server token create --name "test"` creates token and displays it
- [ ] `pc-server token create --name "test" --expires 24h` creates expiring token
- [ ] `pc-server token list` shows all tokens
- [ ] `pc-server token revoke --name "test"` revokes the token
- [ ] `pc-server help` shows usage
- [ ] Unknown commands show error

## Acceptance Criteria

- [ ] `serve` command starts the HTTP server
- [ ] `token create` generates and displays new token
- [ ] `token list` displays all tokens in table format
- [ ] `token revoke` deactivates a token
- [ ] All commands respect `--config` flag
- [ ] Help text is clear and complete
- [ ] Error messages are informative
