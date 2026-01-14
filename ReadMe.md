# PC - Package Checker

This program aims to improve the quality of data publications via running a few simple tests to ensure best practices for publishing data are being followed.

Checks are run by file / respository (data package).
Currently only a few checks are implemented:

**By file:**
- HasOnlyASCII (for filenames)
- HasNoWhiteSpace (for filenames)
- IsFreeOfKeywords (checking file contents); non binary, .xlsx and .docx are supported
- IsValidName (checking if nonsense files are present eg: .Rhistory)
- HasFileNameSpecialChars (~!?@#$%^&*`;,'"()<>[]{})
- IsFileNameTooLong (>64 is too long)

Archives (.zip, .tar, .7z) are also supported. On these the content (IsFreeOfKeywords) on each file is checked if the file is not too big.
As *.tar.gz* files require complete unpacking of the archive to access the list of contained files it is not supported as it would be too slow for large archives.

**By respository:**
- HasReadme (a readme file exists in the repository)
- ReadMeContainsTOC (readme mentions each file containted in the repository)

How the files are passed to the tool is defined via collectors. Currently the `LocaleCollector` and the `CkanCollector` can be used. 
- the `LocalCollector` reads files from your local file system. 
- the `CkanCollector` parses CKAN packages via their name. It determines resources in that package via a webrequest to the CKAN API. The resources are then also read locally. This means that the package checker needs to be deployed on the production server of CKAN, so that the package resources are readable.

## Configuration

The configuration is specified in TOML format. Each test can be configured with:
- `blacklist`: File paths matching these patterns are excluded from the test
- `whitelist`: Only file paths matching these patterns are included in the test
- `keywordArguments`: Test-specific arguments

### Important: Regex vs Literal String Usage

**Regex patterns are ONLY supported in `blacklist` and `whitelist` fields** for file path filtering:

```toml
[test.IsFreeOfKeywords]
# These support regex patterns for file path matching
blacklist = [".*\\.log$", "temp.*", "test[0-9]+\\.txt"]
whitelist = ["src/.*\\.go", "docs/.*\\.md"]
```

**Keywords and disallowed names use LITERAL string matching only:**

```toml
[test.IsFreeOfKeywords]
keywordArguments = [
    # These are literal strings (case-insensitive)
    { keywords = ["password", "api_key", "secret"], info = "Sensitive data found:" },
    { keywords = ["/Users/", "C:\\"], info = "Hardcoded paths found:" }
]

[test.IsValidName]
keywordArguments = [
    # These are literal filename matches
    { disallowed_names = [".DS_Store", "__pycache__", ".vscode"] }
]
```

**DO NOT use regex patterns in keywords** - they will be treated as literal strings:
- ❌ `"pass.*"` will look for the literal text "pass.*"
- ❌ `"[Pp]assword"` will look for the literal text "[Pp]assword"
- ✅ `"password"` will find "password", "Password", "PASSWORD", etc.

### Performance Optimizations

The tool includes several performance optimizations:
- **Fast string matching** for keyword detection (100x+ faster than regex)
- **Parallel processing** for multiple files using worker pools
- **Streaming I/O** for large files to reduce memory usage
- **Memory limits** for archive processing to prevent excessive resource usage
- **Message truncation** to limit output when many similar issues are found

## Run
Set up the package checker configuration:
```bash
cp pc.toml.example pc.toml
```

Once you edited the necessary config you can run with:
```bash
go run main.go
```

or you compile first and run via:
```bash
pc -location your-ckan-package-name
```

run with Terminal User Interface:
```bash
pc -config pc.toml -location .  --tui
```

run with html output:
```bash
pc -config pc.toml -location .  --html report.html
```

run with plain output:
```bash
pc -config pc.toml -location .  --plain
```

## Building
To build (https://github.com/confluentinc/confluent-kafka-go/issues/1092#issuecomment-2373681430): 
```bash
go build -ldflags="-s -w" . && ./pc
```

## Deployment with CKAN
If you want to use the CKAN collector the binary needs to have access to the resources locally, so it can read them without downloading. Make sure the access rights for the binary are set correctly.

Eg:
```bash
# copy to ckan server
scp pc production-ckan:/home/rdm
# change owner to owner of resources
sudo chown ckan:ckan pc
# set the sticky bit, so anyone can run the binary as the user ckan
sudo chmod u+s pc
```

To run the tool from another computer one could:
```bash
#!/usr/bin/bash
echo -e "\e[31m=>This script is running a binary on prod2!\e[0m"
ssh -i .../.ssh/id_ed25519_ckool rdm@production-ckan /home/rdm/pc "$@"
```

## REST API Server

The package checker includes a REST API server (`pc-server`) for remote package checking. This is useful for integrating package checks into web applications or automated workflows.

### Building the Server

```bash
go build -o pc-server ./cmd/pc-server
```

### Running the Server

```bash
pc-server -config ./pc.toml -addr :8080
```

**Flags:**
- `-config` - Path to PC config file (required, or auto-detected from pc.toml)
- `-addr` - Server listen address (default: `:8080`)
- `-ckan-url` - Override CKAN base URL from config
- `-help` - Show usage information

### API Endpoints

#### Health Check
```
GET /health
```

Response:
```json
{
  "status": "ok",
  "version": "1.0.0",
  "timestamp": "2024-01-14T10:30:00Z"
}
```

#### Analyze Package
```
POST /api/v1/analyze
```

**Headers:**
- `Authorization: Bearer <your-ckan-api-token>` (required)
- `Content-Type: application/json`

**Request Body:**
```json
{
  "package_id": "my-ckan-package-id",
  "ckan_url": "https://ckan.example.com"  // optional, overrides server config
}
```

**Response:** Same JSON structure as `pc --json` output.

### Authentication

The server uses pass-through CKAN token authentication. When you send your CKAN API token, the server verifies you have read access to the requested package by calling CKAN's `package_show` API. This ensures users can only check packages they have permission to view.

### Example Usage

```bash
# Start the server
pc-server -config ./pc.toml

# Health check
curl http://localhost:8080/health

# Analyze a package (use your CKAN API token)
curl -X POST http://localhost:8080/api/v1/analyze \
  -H "Authorization: Bearer <your-ckan-api-token>" \
  -H "Content-Type: application/json" \
  -d '{"package_id": "my-package"}'
```

### Error Responses

| Status | Code | Description |
|--------|------|-------------|
| 400 | `invalid_json` | Malformed JSON in request body |
| 400 | `missing_package_id` | No package_id provided |
| 401 | `missing_token` | No Authorization header |
| 401 | `invalid_token_format` | Invalid Bearer token format |
| 403 | `access_denied` | No access to the requested package |
| 404 | `package_not_found` | Package does not exist |
| 500 | `no_ckan_url` | CKAN URL not configured |
| 500 | `internal_error` | Server-side error during check |

### Running TUI over SSH

When running the package checker with TUI interface over SSH, you need to ensure proper terminal allocation:

**Basic SSH execution with TUI:**
```bash
ssh -t user@remote-server "cd /path/to/pc && ./pc"
```

**For better terminal compatibility:**
```bash
ssh -t user@remote-server "export TERM=xterm-256color && cd /path/to/pc && ./pc"
```

**With full environment setup:**
```bash
ssh -t user@remote-server "TERM=xterm-256color LANG=en_US.UTF-8 cd /path/to/pc && ./pc"
```

**Troubleshooting TUI Issues:**

- **Garbled display**: Try different TERM values:
  ```bash
  ssh -t user@remote-server "TERM=screen cd /path/to/pc && ./pc"
  ssh -t user@remote-server "TERM=xterm cd /path/to/pc && ./pc"
  ssh -t user@remote-server "TERM=vt100 cd /path/to/pc && ./pc"
  ```

- **No arrow key navigation**: Ensure your local terminal supports the TERM type being used. Modern terminals like iTerm2, Windows Terminal, or GNOME Terminal work best.

- **Color issues**: Use `TERM=xterm-256color` for full color support, or `TERM=xterm` for basic colors.

- **Using tmux/screen**: For persistent sessions:
  ```bash
  ssh user@remote-server
  tmux new-session "cd /path/to/pc && ./pc"
  ```

**Important**: The `-t` flag is essential as it allocates a pseudo-terminal required for interactive TUI applications.


## Testing
```
go test ./...
```

