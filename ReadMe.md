# PC - Package Checker

This program aims to improve the quality of data publications via running a few simple tests to ensure best practices for publishing data are being followed.

Checks are run by file / respository (data package).
Currently only a few checks are implemented:

**By file:**
- HasOnlyASCII (for filenames)
- HasNoWhiteSpace (for filenames)
- IsFreeOfKeywords (checking file contents); non binary, .xlsx and .docx are supported
- IsValidName (checking if nonsense files are present eg: .Rhistory)

Archives (.zip, .tar, .7z) are also supported. On these the content (IsFreeOfKeywords) on each file is **not** checked.
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


## Testing
```
go test ./...
```