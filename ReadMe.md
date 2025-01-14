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

How the files are passed to the tool is defined via collectors. Currently only the LocaleCollector can be used. Work on the a collector from CKAN via package_id is underway.

## Run
```bash
cp pc.toml.example pc.toml
go run main.go
```

## Building
To build (https://github.com/confluentinc/confluent-kafka-go/issues/1092#issuecomment-2373681430): 
```bash
go build -ldflags="-s -w" . && ./pc
```

## Testing
```
go test ./...
```