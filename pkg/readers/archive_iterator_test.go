package readers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
)

func TestIterareUnpackedFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Test with zip file", "../../testdata/archives/test.zip"},
		{"Test with tar file", "../../testdata/archives/test.tar"},
		{"Test with 7z file", "../../testdata/archives/test.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
			count := 0
			for nfi.HasNext() {
				nfi.Next()
				name, _, _ := nfi.UnpackedFile()
				assert.NotEmpty(t, name, "File name should not be empty")
				count++
			}
			assert.Equal(t, 1, count, "1 File expected in archive, as the second one is empty.")
		})
	}
}

func TestValidFileCount(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Test with zip file", "../../testdata/archives/ten_valid_files.zip"},
		{"Test with tar file", "../../testdata/archives/ten_valid_files.tar"},
		{"Test with 7z file", "../../testdata/archives/ten_valid_files.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
			count := 0
			for nfi.HasNext() {
				nfi.Next()
				name, _, _ := nfi.UnpackedFile()
				assert.NotEmpty(t, name, "File name should not be empty")
				count++
			}
			assert.Equal(t, 10, count, "10 files expected in archive.")
		})
	}
}

func TestIterareEmpty(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"Empty zip", "../../testdata/archives/only_folders.zip"},
		{"Empty tar", "../../testdata/archives/only_folders.tar"},
		{"Empty 7z", "../../testdata/archives/only_folders.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			assert.False(t, nfi.HasFilesToUnpack(), "Expected no valid files in archive")
			assert.False(t, nfi.HasNext())
		})
	}
}

func TestIterareUnpackedFilesMaxSize(t *testing.T) {
	tests := []struct {
		name        string
		filepath    string
		maxLen      int
		expectedLen int
	}{
		{"Zip with 2 files excluded (one empty, one too large)", "../../testdata/archives/test.zip", 5, 0},
		{"Zip with one file accepted (one empty)", "../../testdata/archives/test.zip", 10, 1},
		{"Tar with 2 files excluded (one empty, one too large)", "../../testdata/archives/test.tar", 5, 0},
		{"Tar with one file accepted (one empty)", "../../testdata/archives/test.tar", 10, 1},
		{"7z with 2 files excluded (one empty, one too large)", "../../testdata/archives/test.7z", 5, 0},
		{"7z wwith one file accepted (one empty)", "../../testdata/archives/test.7z", 10, 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: int64(test.maxLen), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})

			if !nfi.HasFilesToUnpack() {
				assert.Equal(t, 0, test.expectedLen, "No files to unpack, but expected some")
				return
			}

			count := 0
			for nfi.HasNext() {
				assert.True(t, nfi.Next())
				count++
			}

			assert.Equal(t, test.expectedLen, count)
		})
	}
}

func TestIteratorEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		filepath    string
		maxSize     int
		expectedLen int
	}{
		{"Empty ZIP", "../../testdata/archives/empty.zip", 1024, 0},
		{"Empty TAR", "../../testdata/archives/empty.tar", 1024, 0},
		{"Empty 7Z", "../../testdata/archives/empty.7z", 1024, 0},
		{"Huge ZIP", "../../testdata/archives/huge_file.zip", 1024, 0},
		{"Huge TAR", "../../testdata/archives/huge_file.tar", 1024, 0},
		{"Huge 7Z", "../../testdata/archives/huge_file.7z", 1024, 0},
		{"Mixed ZIP", "../../testdata/archives/mixed.zip", 1024, 1},
		{"Mixed TAR", "../../testdata/archives/mixed.tar", 1024, 1},
		{"Mixed 7Z", "../../testdata/archives/mixed.7z", 1024, 1},
		{"Mixed ZIP All", "../../testdata/archives/mixed.zip", 20000, 2},
		{"Mixed TAR All", "../../testdata/archives/mixed.tar", 20000, 2},
		{"Mixed 7Z All", "../../testdata/archives/mixed.7z", 20000, 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: int64(test.maxSize), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})

			if test.expectedLen == 0 {
				assert.False(t, nfi.HasFilesToUnpack(), "Expected no files in archive")
				assert.False(t, nfi.HasNext())
				return
			}

			assert.True(t, nfi.HasFilesToUnpack(), "Expected files in archive")

			count := 0
			for nfi.HasNext() {
				assert.True(t, nfi.Next(), "Expected valid file")
				assert.NotEmpty(t, nfi.CurrentFilename)
				assert.Greater(t, len(nfi.CurrentFileContent), 0)
				count++
			}

			assert.Equal(t, test.expectedLen, count)
			assert.False(t, nfi.HasNext())
		})
	}
}

func TestFiltersDuringArchiveIteration(t *testing.T) {
	// The one_of_each archives contain:
	// - an empty file
	// - a valid file with a size of 175 kB
	// - a valid file with a size of 1.2 MB
	// - a valid file with a size of 2.3 MB
	// - a binary file with a size of 1 MB
	// - a valid file to whitelist
	// - a valid file to blacklist
	baseTests := []struct {
		name          string
		baseFile      string
		maxLen        int
		whitelist     []string
		blacklist     []string
		unpackedFiles []string
	}{
		{"Archive with maxSize filter", "one_of_each", 2 * 1024 * 1024, []string{}, []string{}, []string{"large_valid.txt", "very_large_but_valid.txt", "black/to_be_blacklisted.blst", "white/to_be_whitelisted.wlst"}},
		{"Archive with smaller maxSize filter", "one_of_each", 0.5 * 1024 * 1024, []string{}, []string{}, []string{"large_valid.txt", "black/to_be_blacklisted.blst", "white/to_be_whitelisted.wlst"}},
		{"Archive with whitelist filter", "one_of_each", 2 * 1024 * 1024, []string{".wlst"}, []string{}, []string{"white/to_be_whitelisted.wlst"}},
		{"Archive with whitelist filter 2", "one_of_each", 2 * 1024 * 1024, []string{"to_be_whitelisted"}, []string{}, []string{"white/to_be_whitelisted.wlst"}},
		{"Archive with blacklist filter", "one_of_each", 2 * 1024 * 1024, []string{}, []string{".blst"}, []string{"large_valid.txt", "very_large_but_valid.txt", "white/to_be_whitelisted.wlst"}},
		{"Archive with overlapping filters", "one_of_each", 0.5 * 1024 * 1024, []string{}, []string{".blst"}, []string{"large_valid.txt", "white/to_be_whitelisted.wlst"}},
		{"Archive with overlapping filters 2", "one_of_each", 10, []string{"wlst"}, []string{}, []string{}},
	}

	var tests []struct {
		name          string
		filepath      string
		maxLen        int
		whitelist     []string
		blacklist     []string
		unpackedFiles []string
	}

	formats := []string{".zip", ".7z", ".tar"}

	for _, base := range baseTests {
		for _, ext := range formats {
			tests = append(tests, struct {
				name          string
				filepath      string
				maxLen        int
				whitelist     []string
				blacklist     []string
				unpackedFiles []string
			}{
				name:          fmt.Sprintf("%s (%s)", base.name, ext),
				filepath:      fmt.Sprintf("../../testdata/archives/%s%s", base.baseFile, ext),
				maxLen:        base.maxLen,
				whitelist:     base.whitelist,
				blacklist:     base.blacklist,
				unpackedFiles: base.unpackedFiles,
			})
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: int64(test.maxLen), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, test.whitelist, test.blacklist)
			if len(test.unpackedFiles) == 0 {
				assert.False(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
			} else {
				assert.True(t, nfi.HasFilesToUnpack(), "Expected archive to have valid files")
				count := 0
				for nfi.HasNext() {
					assert.True(t, nfi.Next())
					name, _, _ := nfi.UnpackedFile()

					// Check if the file name is in the unpacked_files list
					found := false
					for _, unpackedFile := range test.unpackedFiles {
						if name == unpackedFile {
							found = true
							break
						}
					}
					assert.True(t, found, fmt.Sprintf("File '%s' should be in the unpacked_files list", name))
					count++
				}

				assert.Equal(t, len(test.unpackedFiles), count, "Number of unpacked files should match the expected count")
			}
		})
	}
}

func TestSkippingALotOfFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"A lot of empty files in ZIP", "../../testdata/archives/a_lot_of_empty_files.zip"},
		{"A lot of empty files in TAR", "../../testdata/archives/a_lot_of_empty_files.tar"},
		{"A lot of empty files in 7Z", "../../testdata/archives/a_lot_of_empty_files.7z"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			assert.False(t, nfi.HasFilesToUnpack(), "Expected no files in archive")
			assert.False(t, nfi.HasNext())
		})
	}
}

func TestALotOfBinaryFiles(t *testing.T) {
	tests := []struct {
		name     string
		filepath string
	}{
		{"A lot of empty files in ZIP", "../../testdata/archives/a_lot_of_binary_files.zip"},
		{"A lot of empty files in 7z", "../../testdata/archives/a_lot_of_binary_files.7z"},
		{"A lot of empty files in TAR", "../../testdata/archives/a_lot_of_binary_files.tar"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			assert.False(t, nfi.HasFilesToUnpack(), "Expected only binary files in archive, so nothing to read")
			assert.False(t, nfi.HasNext())
		})
	}
}

// drainIterator fully iterates an archive and returns the skip acknowledgements
// emitted for members that were not content-scanned.
func drainIterator(nfi *UnpackedFileIterator) []structs.Message {
	if nfi.HasFilesToUnpack() {
		for nfi.HasNext() {
			nfi.Next()
		}
	}
	return nfi.SkipMessages()
}

func TestArchiveIterator_MemberSizeSkipEmitsMessages(t *testing.T) {
	formats := []string{".zip", ".tar", ".7z"}
	for _, ext := range formats {
		t.Run("size skip "+ext, func(t *testing.T) {
			path := "../../testdata/archives/one_of_each" + ext
			filename := "one_of_each" + ext

			// maxLen 0.5MB excludes the 1.2MB and 2.3MB members by size while the
			// total-memory budget (generous) is not the cause.
			nfi := InitArchiveIterator(path, filename, ArchiveLimits{MaxMemberSize: int64(0.5 * 1024 * 1024), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			skips := drainIterator(nfi)

			if len(skips) == 0 {
				t.Fatalf("expected at least one member size-skip message for %s", ext)
			}
			for _, m := range skips {
				if !m.Skipped {
					t.Errorf("expected Skipped=true, got %+v", m)
				}
				if m.Reason == "" {
					t.Errorf("expected non-empty Reason, got %+v", m)
				}
				src, ok := m.Source.(structs.File)
				if !ok {
					t.Fatalf("expected File source, got %T", m.Source)
				}
				if src.ArchiveName != filename {
					t.Errorf("expected ArchiveName=%q, got %q", filename, src.ArchiveName)
				}
				if !strings.Contains(m.Content, "exceeds maximum archive member size") {
					t.Errorf("unexpected size-skip content: %q", m.Content)
				}
			}
		})
	}
}

// sniffTrapReader serves its data and fails the test on any read past it —
// structural proof that binary members are never read beyond the sample.
type sniffTrapReader struct {
	t    *testing.T
	data []byte
	pos  int
}

func (r *sniffTrapReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		r.t.Fatal("sniffThenRead read past the 512-byte sample on a binary member")
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestSniffThenRead(t *testing.T) {
	it := InitArchiveIterator("x", "x.zip", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil, nil)
	text := func(n int) string { return strings.Repeat("a", n) }

	t.Run("binary aborts after sample", func(t *testing.T) {
		r := &sniffTrapReader{t: t, data: make([]byte, 512)}
		isText, content, overrun, err := it.sniffThenRead(r, 1<<20)
		assert.NoError(t, err)
		assert.False(t, isText)
		assert.False(t, overrun)
		assert.Nil(t, content)
	})
	t.Run("text exact declared size", func(t *testing.T) {
		isText, content, overrun, err := it.sniffThenRead(strings.NewReader(text(600)), 600)
		assert.NoError(t, err)
		assert.True(t, isText)
		assert.False(t, overrun)
		assert.Equal(t, text(600), string(content))
	})
	t.Run("text shorter than declared", func(t *testing.T) {
		isText, content, overrun, err := it.sniffThenRead(strings.NewReader(text(600)), 1000)
		assert.NoError(t, err)
		assert.True(t, isText)
		assert.False(t, overrun)
		assert.Equal(t, text(600), string(content))
	})
	t.Run("tiny text member", func(t *testing.T) {
		isText, content, overrun, err := it.sniffThenRead(strings.NewReader(text(10)), 10)
		assert.NoError(t, err)
		assert.True(t, isText)
		assert.False(t, overrun)
		assert.Equal(t, text(10), string(content))
	})
	t.Run("stream longer than declared is overrun", func(t *testing.T) {
		isText, content, overrun, err := it.sniffThenRead(strings.NewReader(text(700)), 600)
		assert.NoError(t, err)
		assert.True(t, isText)
		assert.True(t, overrun)
		assert.Nil(t, content)
	})
	t.Run("sample already exceeds declared is overrun", func(t *testing.T) {
		isText, content, overrun, err := it.sniffThenRead(strings.NewReader(text(600)), 10)
		assert.NoError(t, err)
		assert.True(t, isText)
		assert.True(t, overrun)
		assert.Nil(t, content)
	})
	t.Run("empty stream is not text", func(t *testing.T) {
		isText, _, _, err := it.sniffThenRead(strings.NewReader(""), 100)
		assert.NoError(t, err)
		assert.False(t, isText)
	})
}

func TestZipBinaryBombAbortedAfterSniff(t *testing.T) {
	// Deflate bomb with a corrupt tail: one stored block of 4096 binary bytes,
	// then a reserved block type. Reading past the sample would error; the
	// sniff-abort path never touches the corrupt region and iteration proceeds
	// cleanly to the text member behind it.
	var raw bytes.Buffer
	raw.WriteByte(0x00) // BFINAL=0, BTYPE=00 (stored)
	assert.NoError(t, binary.Write(&raw, binary.LittleEndian, uint16(4096)))
	assert.NoError(t, binary.Write(&raw, binary.LittleEndian, ^uint16(4096)))
	raw.Write(make([]byte, 4096))
	raw.Write([]byte{0x07, 0xff, 0xff, 0xff}) // BFINAL=1, BTYPE=11 (reserved) = corrupt

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "bomb.bin",
		Method:             zip.Deflate,
		UncompressedSize64: 1 << 20,
		CompressedSize64:   uint64(raw.Len()),
	})
	assert.NoError(t, err)
	_, err = w.Write(raw.Bytes())
	assert.NoError(t, err)
	tw, err := zw.Create("readme.txt")
	assert.NoError(t, err)
	text := []byte(strings.Repeat("plain text content\n", 40))
	_, err = tw.Write(text)
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())

	path := filepath.Join(t.TempDir(), "bomb.zip")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	nfi := InitArchiveIterator(path, "bomb.zip", ArchiveLimits{MaxMemberSize: 2 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	var names []string
	for nfi.HasNext() {
		nfi.Next()
		name, content, _ := nfi.UnpackedFile()
		names = append(names, name)
		assert.Equal(t, text, content)
	}
	assert.Equal(t, []string{"readme.txt"}, names)
	assert.Equal(t, int64(len(text)), nfi.totalMemoryUsed, "bomb must not be charged")
	assert.Empty(t, nfi.SkipMessages())
}

func TestArchiveIterator_7zDeclaredSizeGate(t *testing.T) {
	path := "../../testdata/archives/one_of_each.7z"

	// Declared sum ~4.7 MB; budget 1 MB -> 4 MB gate limit -> rejected before
	// any decompression.
	nfi := InitArchiveIterator(path, "one_of_each.7z", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
	assert.False(t, nfi.HasFilesToUnpack())
	skips := nfi.SkipMessages()
	if assert.Len(t, skips, 1) {
		m := skips[0]
		assert.True(t, m.Skipped)
		assert.Contains(t, m.Content, "declared uncompressed size")
		src, ok := m.Source.(structs.File)
		if assert.True(t, ok) {
			assert.Equal(t, "", src.ArchiveName, "archive-level ack must not nest the archive in itself")
		}
	}
	assert.Equal(t, int64(0), nfi.totalMemoryUsed)
	assert.Equal(t, 0, nfi.processedFileCount)

	// Budget above sum/4: gate passes, scan proceeds.
	ok := InitArchiveIterator(path, "one_of_each.7z", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 2 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
	assert.True(t, ok.HasFilesToUnpack())
}

func writeTarGzFixture(t *testing.T, path string, members []struct {
	name    string
	content []byte
}) {
	t.Helper()
	f, err := os.Create(path)
	assert.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, m := range members {
		assert.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     m.name,
			Mode:     0o600,
			Size:     int64(len(m.content)),
			Typeflag: tar.TypeReg,
		}))
		_, err = tw.Write(m.content)
		assert.NoError(t, err)
	}
	assert.NoError(t, tw.Close())
	assert.NoError(t, gw.Close())
	assert.NoError(t, f.Close())
}

func TestTarGzNormalScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.tar.gz")
	text := []byte(strings.Repeat("text line\n", 1000))
	writeTarGzFixture(t, path, []struct {
		name    string
		content []byte
	}{{"a.txt", text}, {"b.txt", text}})

	nfi := InitArchiveIterator(path, "ok.tar.gz", ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		_, content, _ := nfi.UnpackedFile()
		assert.Equal(t, text, content)
		count++
	}
	assert.Equal(t, 2, count)
	assert.Equal(t, int64(2*len(text)), nfi.totalMemoryUsed)
}

func TestTarGzWalkCapStopsIteration(t *testing.T) {
	// Three 2 MB members with a 1 MB budget: every member is memory-skipped,
	// but the drains still count against the 4 MB walk cap -> iteration stops
	// with an archive-level acknowledgement instead of decompressing all 6 MB.
	path := filepath.Join(t.TempDir(), "big.tar.gz")
	big := bytes.Repeat([]byte("A"), 2*1024*1024)
	writeTarGzFixture(t, path, []struct {
		name    string
		content []byte
	}{{"m1.txt", big}, {"m2.txt", big}, {"m3.txt", big}})

	nfi := InitArchiveIterator(path, "big.tar.gz", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
	assert.False(t, nfi.HasFilesToUnpack())

	foundWalkStop := false
	for _, m := range nfi.SkipMessages() {
		if strings.Contains(m.Content, "Stopped content scan") {
			foundWalkStop = true
		}
	}
	assert.True(t, foundWalkStop, "expected walk-cap acknowledgement, got %+v", nfi.SkipMessages())
	assert.LessOrEqual(t, nfi.walkCounter.count, nfi.walkCounter.limit+64*1024, "decompression must stop within one chunk of the cap")
}

func TestTarGzWalkCapSingleHugeMember(t *testing.T) {
	// One member alone busts the cap: the header pre-check stops the scan
	// before ANY of its content is decompressed.
	path := filepath.Join(t.TempDir(), "huge.tar.gz")
	writeTarGzFixture(t, path, []struct {
		name    string
		content []byte
	}{{"huge.txt", bytes.Repeat([]byte("B"), 20*1024*1024)}})

	nfi := InitArchiveIterator(path, "huge.tar.gz", ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
	assert.False(t, nfi.HasFilesToUnpack())

	foundWalkStop := false
	for _, m := range nfi.SkipMessages() {
		if strings.Contains(m.Content, "Stopped content scan") {
			foundWalkStop = true
		}
	}
	assert.True(t, foundWalkStop)
	assert.Less(t, nfi.walkCounter.count, int64(64*1024), "only tar headers may be decompressed")
}

func TestArchiveIterator_MembersDecompressedAndChargedOnce(t *testing.T) {
	// Regression for the zip/7z double-read bug: the look-ahead buffer was never
	// consumed, so every member was decompressed twice and charged twice against
	// the memory budget (halving the effective budget).
	formats := []string{".zip", ".tar", ".7z"}
	for _, ext := range formats {
		t.Run("single charge "+ext, func(t *testing.T) {
			path := "../../testdata/archives/ten_valid_files" + ext
			filename := "ten_valid_files" + ext

			nfi := InitArchiveIterator(path, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			assert.True(t, nfi.HasFilesToUnpack())
			var contentSum int64
			count := 0
			for nfi.HasNext() {
				nfi.Next()
				_, content, _ := nfi.UnpackedFile()
				contentSum += int64(len(content))
				count++
			}
			assert.Equal(t, 10, count)
			assert.Equal(t, contentSum, nfi.totalMemoryUsed, "each member must be charged exactly once")
			assert.Equal(t, count, nfi.processedFileCount, "each member must be processed exactly once")

			// A budget of exactly the summed content must admit every member.
			tight := InitArchiveIterator(path, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: contentSum, MaxMemberCount: 1000}, []string{}, []string{})
			assert.True(t, tight.HasFilesToUnpack())
			tightCount := 0
			for tight.HasNext() {
				tight.Next()
				tightCount++
			}
			assert.Equal(t, 10, tightCount, "exact-fit budget must not skip members")
			assert.Empty(t, tight.SkipMessages())
		})
	}
}

func TestArchiveIterator_TotalMemorySkipEmitsMessages(t *testing.T) {
	formats := []string{".zip", ".tar", ".7z"}
	for _, ext := range formats {
		t.Run("memory skip "+ext, func(t *testing.T) {
			path := "../../testdata/archives/one_of_each" + ext
			filename := "one_of_each" + ext

			// Large per-member size limit but a small total-memory budget: members
			// are rejected by the memory budget rather than their individual size.
			// The budget is chosen above declaredSum/declaredSizeBudgetMultiple so
			// the 7z declared-size gate does NOT trip (that path has its own test)
			// while the 2.3 MB member still exceeds the remaining budget.
			nfi := InitArchiveIterator(path, filename, ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 1536 * 1024, MaxMemberCount: 1000}, []string{}, []string{})
			skips := drainIterator(nfi)

			foundMemorySkip := false
			for _, m := range skips {
				if !m.Skipped {
					t.Errorf("expected Skipped=true, got %+v", m)
				}
				if strings.Contains(m.Content, "declared uncompressed size") {
					t.Errorf("declared-size gate must not trip in this test, got %q", m.Content)
				}
				if strings.Contains(m.Content, "total archive memory limit") {
					foundMemorySkip = true
				}
			}
			if !foundMemorySkip {
				t.Errorf("expected at least one total-memory skip message for %s, got %+v", ext, skips)
			}
		})
	}
}
