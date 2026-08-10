package readers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestReadZipFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.zip",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.zip", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.zip"},
			},
		},
	}
	for _, test := range tests {
		actual, truncated, err := ReadZipFileListWithDisplayName(test.filepath, "", 1000)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if truncated {
			t.Errorf("unexpected truncation")
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}

func TestReadTarFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.tar",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar"},
			},
		},
	}
	for _, test := range tests {
		actual, truncated, err := ReadTarFileListWithDisplayName(test.filepath, "", 1000)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if truncated {
			t.Errorf("unexpected truncation")
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}

func TestReadTarGzFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.tar.gz",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar.gz"},
			},
		},
	}
	for _, test := range tests {
		actual, truncated, err := ReadTarGzFileListWithDisplayName(test.filepath, "", 1000, 100*1024*1024)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if truncated {
			t.Errorf("unexpected truncation")
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}
func TestReadArchiveFileList(t *testing.T) {
	tests := []struct {
		file     structs.File
		expected []structs.File
	}{
		{
			file: structs.File{Path: "../../testdata/archives/test.zip", Name: "test.zip", DisplayName: "test.zip", Suffix: ".zip"},
			expected: []structs.File{
				{Path: "../../testdata/archives/test.zip", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.zip"},
				{Path: "../../testdata/archives/test.zip", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.zip"},
			},
		},
		{
			file: structs.File{Path: "../../testdata/archives/test.tar", Name: "test.tar", DisplayName: "test.tar", Suffix: ".tar"},
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar"},
				{Path: "../../testdata/archives/test.tar", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar"},
			},
		},
		{
			file: structs.File{Path: "../../testdata/archives/test.tar.gz", Name: "test.tar.gz", DisplayName: "test.tar.gz", Suffix: ".gz"},
			expected: []structs.File{
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.tar.gz"},
				{Path: "../../testdata/archives/test.tar.gz", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.tar.gz"},
			},
		},
		{
			file:     structs.File{Path: "../../testdata/file.unsupported-suffix", Name: "config.toml.test", DisplayName: "config.toml.test", Suffix: ".test"},
			expected: []structs.File{},
		},
	}
	for _, test := range tests {
		actual, truncated, err := ReadArchiveFileList(test.file, 1000, 100*1024*1024)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if truncated {
			t.Errorf("unexpected truncation")
		}
		if !reflect.DeepEqual(actual, test.expected) {
			t.Errorf("Expected: %v, Actual: %v", test.expected, actual)
		}
	}
}
func TestRead7ZipFileList(t *testing.T) {
	tests := []struct {
		filepath string
		expected []structs.File
	}{
		{
			filepath: "../../testdata/archives/test.7z",
			expected: []structs.File{
				{Path: "../../testdata/archives/test.7z", Name: "test/", DisplayName: "test/", Size: 0, Suffix: "", ArchiveName: "test.7z"},
				{Path: "../../testdata/archives/test.7z", Name: "test/file2", DisplayName: "test/file2", Size: 0, Suffix: "", ArchiveName: "test.7z"},
				{Path: "../../testdata/archives/test.7z", Name: "test/file1.txt", DisplayName: "test/file1.txt", Size: 6, Suffix: ".txt", ArchiveName: "test.7z"},
			},
		},
	}
	for _, test := range tests {
		actual, truncated, err := Read7ZipFileListWithDisplayName(test.filepath, "", 1000)
		if err != nil {
			t.Errorf("Error: %v", err)
		}
		if truncated {
			t.Errorf("unexpected truncation")
		}
		if !reflect.DeepEqual(actual, test.expected) {
			assert.ElementsMatch(t, actual, test.expected)
		}
	}
}

// TestReadArchiveFileListMemberCap: every format stops the walk at maxMembers
// and reports it, and the fixtures list completely one member above the cap.
func TestReadArchiveFileListMemberCap(t *testing.T) {
	// Each fixture holds 3 entries.
	fixtures := []structs.File{
		{Path: "../../testdata/archives/test.zip", Name: "test.zip", DisplayName: "test.zip", Suffix: ".zip"},
		{Path: "../../testdata/archives/test.tar", Name: "test.tar", DisplayName: "test.tar", Suffix: ".tar"},
		{Path: "../../testdata/archives/test.tar.gz", Name: "test.tar.gz", DisplayName: "test.tar.gz", Suffix: ".gz"},
		{Path: "../../testdata/archives/test.7z", Name: "test.7z", DisplayName: "test.7z", Suffix: ".7z"},
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			list, truncated, err := ReadArchiveFileList(f, 2, 100*1024*1024)
			assert.NoError(t, err)
			assert.True(t, truncated, "3 members must bust a limit of 2")
			assert.Nil(t, list, "a truncated walk must not hand back a partial list")

			list, truncated, err = ReadArchiveFileList(f, 3, 100*1024*1024)
			assert.NoError(t, err)
			assert.False(t, truncated)
			assert.Len(t, list, 3)
		})
	}
}

// TestReadArchiveFileListFailsClosed: a non-positive limit means "nothing
// qualifies", never "unlimited".
func TestReadArchiveFileListFailsClosed(t *testing.T) {
	f := structs.File{Path: "../../testdata/archives/test.zip", Name: "test.zip", DisplayName: "test.zip", Suffix: ".zip"}
	_, truncated, err := ReadArchiveFileList(f, 0, 100*1024*1024)
	assert.NoError(t, err)
	assert.True(t, truncated)
}

// TestReadTarGzFileListWalkCap: a small .tar.gz whose members declare a huge
// expansion stops the walk on the byte budget, before the member cap could ever
// fire - the gzip-bomb case a member count alone never catches.
func TestReadTarGzFileListWalkCap(t *testing.T) {
	members := []struct {
		name    string
		content []byte
	}{}
	for _, name := range []string{"m1.txt", "m2.txt", "m3.txt", "m4.txt"} {
		members = append(members, struct {
			name    string
			content []byte
		}{name, bytes.Repeat([]byte("A"), 100*1024)})
	}
	path := filepath.Join(t.TempDir(), "bomb.tar.gz")
	writeTarGzFixture(t, path, members)

	// Budget 4 x 64 KiB = 256 KiB: the third member's declared size busts it.
	list, truncated, err := ReadTarGzFileListWithDisplayName(path, "bomb.tar.gz", 1000, 64*1024)
	assert.NoError(t, err)
	assert.True(t, truncated)
	assert.Nil(t, list)

	// Same archive, honest budget: the walk completes.
	list, truncated, err = ReadTarGzFileListWithDisplayName(path, "bomb.tar.gz", 1000, 1024*1024)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, list, 4)
}

// writeTarGzHeaderOnly writes a .tar.gz holding one member HEADER that declares
// size bytes and no body at all (tw.Close is skipped on purpose - it would
// demand the missing bytes). Only a check on header.Size can end such a walk
// cleanly; draining the body hits an unexpected EOF instead.
func writeTarGzHeaderOnly(t *testing.T, path string, name string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	assert.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	assert.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg}))
	assert.NoError(t, gw.Close())
	assert.NoError(t, f.Close())
}

// TestReadTarGzFileListHeaderPreCheck pins the pre-check that stops the walk on
// the DECLARED size: the member's body is absent, so the (truncated, err) pair
// below is only reachable without pulling body bytes. Delete the pre-check and
// the drain fails with an unexpected EOF instead.
func TestReadTarGzFileListHeaderPreCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "declared.tar.gz")
	writeTarGzHeaderOnly(t, path, "huge.txt", 8*1024*1024)

	list, truncated, err := ReadTarGzFileListWithDisplayName(path, "declared.tar.gz", 1000, 64*1024)
	assert.NoError(t, err)
	assert.True(t, truncated)
	assert.Nil(t, list)
}

// TestReadTarGzFileListWalkCapDuringRead covers the counter arm: many tiny
// members against a tiny budget trip the byte cap inside tar.Next (padding and
// header blocks are charged too), which must read as truncation, not an error.
func TestReadTarGzFileListWalkCapDuringRead(t *testing.T) {
	var members []struct {
		name    string
		content []byte
	}
	for i := 0; i < 8; i++ {
		members = append(members, struct {
			name    string
			content []byte
		}{fmt.Sprintf("m%d.txt", i), []byte("x")})
	}
	path := filepath.Join(t.TempDir(), "many.tar.gz")
	writeTarGzFixture(t, path, members)

	list, truncated, err := ReadTarGzFileListWithDisplayName(path, "many.tar.gz", 1000, 500)
	assert.NoError(t, err)
	assert.True(t, truncated)
	assert.Nil(t, list)
}

// TestReadTarGzFileListBudgetSaturates: a huge configured memory budget must
// saturate instead of wrapping the walk budget negative, which would truncate
// every tar.gz.
func TestReadTarGzFileListBudgetSaturates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.tar.gz")
	writeTarGzFixture(t, path, []struct {
		name    string
		content []byte
	}{{"a.txt", []byte("hello")}})

	list, truncated, err := ReadTarGzFileListWithDisplayName(path, "small.tar.gz", 1000, math.MaxInt64)
	assert.NoError(t, err)
	assert.False(t, truncated)
	assert.Len(t, list, 1)
}
