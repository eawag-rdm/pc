package readers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"strings"

	"github.com/eawag-rdm/pc/pkg/structs"

	"github.com/bodgit/sevenzip"
)

// walkByteBudget is the decompressed-byte budget of a tar.gz file-list walk.
// It saturates: declaredSizeBudgetMultiple * maxTotalMemory wraps negative for
// a huge configured budget, which would truncate every archive. Non-positive
// input stays fail-closed.
func walkByteBudget(maxTotalMemory int64) int64 {
	if maxTotalMemory <= 0 {
		return 0
	}
	if maxTotalMemory > math.MaxInt64/declaredSizeBudgetMultiple {
		return math.MaxInt64
	}
	return declaredSizeBudgetMultiple * maxTotalMemory
}

// ReadZipFileListWithDisplayName reads the file list with archive display name
func ReadZipFileListWithDisplayName(filePath string, archiveDisplayName string, maxMembers int) ([]structs.File, bool, error) {
	// Check if the file exists
	// Open the zip file for reading
	reader, err := zip.OpenReader(filePath)
	if err != nil {

		return nil, false, err
	}
	defer reader.Close()

	// Use archive filename if display name not provided
	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	// Central directory is already parsed: the count is one field read.
	if len(reader.File) > maxMembers {
		return nil, true, nil
	}

	// Read the file list from the zip file
	fileList := make([]structs.File, 0, len(reader.File))
	for _, file := range reader.File {
		f := structs.ToFileWithDisplay(filePath, file.Name, file.Name, file.FileInfo().Size(), "", archiveDisplayName)
		f.RelPath = f.Name
		fileList = append(fileList, f)
	}
	return fileList, false, nil

}

// ReadTarFileListWithDisplayName reads the file list with archive display name.
// No byte cap here: plain tar decompresses nothing and tar.Next SEEKS over
// member bodies, so the walk reads at most the on-disk size (wrapping the file
// would turn every seek into a full read). The member cap bounds entries, not
// memory: a single tar.Next can still buffer a whole GNU sparse-1.0 map before
// the cap is consulted.
func ReadTarFileListWithDisplayName(filePath string, archiveDisplayName string, maxMembers int) ([]structs.File, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	tarReader := tar.NewReader(file)
	var fileList []structs.File
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		if len(fileList) >= maxMembers {
			return nil, true, nil
		}
		f := structs.ToFileWithDisplay(filePath, header.Name, header.Name, header.Size, "", archiveDisplayName)
		f.RelPath = f.Name
		fileList = append(fileList, f)
	}
	return fileList, false, nil
}

// ReadTarGzFileListWithDisplayName reads the file list with archive display
// name, bounded by maxMembers and by the same countingReader budget the content
// walk uses (declaredSizeBudgetMultiple x maxTotalMemory).
func ReadTarGzFileListWithDisplayName(filePath string, archiveDisplayName string, maxMembers int, maxTotalMemory int64) ([]structs.File, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, false, err
	}
	defer gzipReader.Close()

	counter := &countingReader{r: gzipReader, limit: walkByteBudget(maxTotalMemory)}
	tarReader := tar.NewReader(counter)
	var fileList []structs.File
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, errWalkCapExceeded) {
				return nil, true, nil
			}
			return nil, false, err
		}
		// Stop before decompressing a member body that would bust the budget.
		// Subtraction, not addition: a PAX header size of MaxInt64 wraps the sum
		// negative and would slip past the stop.
		if header.Size > counter.limit-counter.count {
			return nil, true, nil
		}
		if len(fileList) >= maxMembers {
			return nil, true, nil
		}
		f := structs.ToFileWithDisplay(filePath, header.Name, header.Name, header.Size, "", archiveDisplayName)
		f.RelPath = f.Name
		fileList = append(fileList, f)
	}
	return fileList, false, nil
}

// Read7ZipFileListWithDisplayName reads the file list with archive display name
func Read7ZipFileListWithDisplayName(filePath string, archiveDisplayName string, maxMembers int) ([]structs.File, bool, error) {
	if archiveDisplayName == "" {
		archiveDisplayName = path.Base(filePath)
	}

	r, err := sevenzip.OpenReader(filePath)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()

	// Header is already parsed: the count is one field read.
	if len(r.File) > maxMembers {
		return nil, true, nil
	}

	fileList := make([]structs.File, 0, len(r.File))
	for _, f := range r.File {
		file := structs.ToFileWithDisplay(filePath, f.Name, f.Name, f.FileInfo().Size(), "", archiveDisplayName)
		file.RelPath = file.Name
		fileList = append(fileList, file)
	}

	return fileList, false, nil
}

func IsSupportedArchive(filePath string) bool {
	if strings.HasSuffix(filePath, ".zip") {
		return true
	} else if strings.HasSuffix(filePath, ".tar") {
		return true
	} else if strings.HasSuffix(filePath, ".7z") {
		return true
	} else if strings.HasSuffix(filePath, ".tar.gz") {
		return true
	}
	return false
}

// ReadArchiveFileList lists an archive's members for the name checks.
// maxMembers and maxTotalMemory come from config.GeneralConfig.ArchiveLimits
// (the only defaulting site); non-positive limits fail closed, never
// "unlimited". Every reader stops at maxMembers counting ALL entries
// (directories included, unlike the content scan's unpack candidates), and
// tar.gz additionally bounds decompressed bytes (tar.Next decompresses THROUGH
// member bodies, so a member cap alone never stops a gzip bomb).
// truncated == true means a bound was busted and the list is nil: unlike the
// content scan, which keeps the members it already scanned, this walk discards
// everything on purpose, so a partial list can never be mistaken for a complete
// one. The caller must skip the archive.
// Every listed member carries the member path VERBATIM in Name and in RelPath -
// a directory member keeps its trailing slash, and no reader normalizes it, so
// one selector never sees two conventions. RelPath is assigned at each
// construction site rather than inherited from ToFile's default, so a change to
// that default cannot silently redefine the subject a path filter matches on;
// it is copied from the CONSTRUCTED Name, which for the nameless member a
// crafted archive can hold is the archive's own base name, not "".
func ReadArchiveFileList(file structs.File, maxMembers int, maxTotalMemory int64) ([]structs.File, bool, error) {
	// Use DisplayName for archive reference (CKAN resource name or filename)
	archiveDisplayName := file.GetDisplayName()

	if strings.HasSuffix(file.Name, ".zip") {
		return ReadZipFileListWithDisplayName(file.Path, archiveDisplayName, maxMembers)
	} else if strings.HasSuffix(file.Name, ".tar") {
		return ReadTarFileListWithDisplayName(file.Path, archiveDisplayName, maxMembers)
	} else if strings.HasSuffix(file.Name, ".7z") {
		return Read7ZipFileListWithDisplayName(file.Path, archiveDisplayName, maxMembers)
	} else if strings.HasSuffix(file.Name, ".tar.gz") {
		return ReadTarGzFileListWithDisplayName(file.Path, archiveDisplayName, maxMembers, maxTotalMemory)
	} else {
		return []structs.File{}, false, nil
	}
}
