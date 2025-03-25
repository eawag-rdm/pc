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