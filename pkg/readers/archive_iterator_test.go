package readers

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eawag-rdm/pc/pkg/selector"
	"github.com/eawag-rdm/pc/pkg/structs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: int64(test.maxLen), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)

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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: int64(test.maxSize), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)

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

// memberSpec is one member filter's declaration: the patterns a row carries,
// under the frame the rows share - matched over the full member path, and
// case-insensitively, which a [[rule]] does not do by default. The frame is the
// fixture's own choice, not a reading anything forces.
//
// Three benchmarks in archive_iterator_bench_test.go compile through it too, so
// editing the frame to suit a table row moves their numbers.
func memberSpec(include, exclude []string) selector.Spec {
	return selector.Spec{Rule: "test", Subject: "path", IgnoreCase: true, Include: include, Exclude: exclude}
}

// mustMemberFilter compiles one member selector through the only constructor
// production has, so a row's verdict is the production matcher's and not a
// test-only reading of it. A compile failure aborts the row - degrading to an
// unfiltered iterator would assert nothing.
func mustMemberFilter(t *testing.T, spec selector.Spec) *selector.Selector {
	t.Helper()
	sel, err := selector.Compile(spec)
	require.NoError(t, err)
	return &sel
}

func TestFiltersDuringArchiveIteration(t *testing.T) {
	// FROZEN TABLE - scope: the ARCHIVE MEMBER FILTER only, i.e. the
	// selector.Spec each row carries, compiled by selector.Compile and read by
	// admitMember -> Selector.MatchScratch, exactly as the two production
	// callers do it. The file-check filter site is a different mechanism,
	// already migrated at HEAD; nothing here asserts anything about it.
	//
	// The rows carried [test.X] whitelist/blacklist lists until the legacy
	// translation was deleted - the row names still say so, because names are
	// frozen - and every pattern below is that translation's output, quoted by
	// hand (`.log` -> `\.log`) under the frame it forced: ignoreCase, subject
	// "path". Each row therefore still asserts the member-filter semantics the
	// Selector migration declared:
	// - a quoted entry still matches as a literal substring, never as a regex,
	//   and still case-insensitively;
	// - still matched over the FULL member path, not the basename;
	// - CHANGED (the one user-visible move of this migration): both lists now
	//   apply together - a member must match the whitelist AND avoid the
	//   blacklist, where a non-empty blacklist used to disable the whitelist
	//   entirely.
	//
	// An empty list entry was inert while the translation dropped it before
	// compiling. The rows carrying one went with the translation, because
	// selector.Compile refuses an empty pattern outright - a refusal
	// TestArchiveFilterRejectsInvalidPatterns pins in their place.
	//
	// Two further declared changes have no row here because no fixture can hold
	// their input:
	// - an EMPTY member name inverts both verdicts. The old matcher treated an
	//   empty subject as "matches", so a blacklist rejected it and a whitelist
	//   admitted it; the selector matches patterns against it like any other
	//   subject, so a blacklist now admits it and a whitelist rejects it. Pinned
	//   by TestAdmitMemberEmptyName.
	// - an entry holding a NON-ASCII byte keeps RE2 simple folding instead of
	//   Unicode ToLower (pinned by selector.TestSelectorLiteralFastPathEquivalence,
	//   whose "test" x "teſt.csv" pair the two schemes disagree on).
	//
	// Legal moves for the Selector migration commit:
	// - the EXPECTATIONS of existing rows MAY change - that diff IS the
	//   user-visible release note for the semantics change;
	// - new rows MAY be appended at the end;
	// - a row MAY be deleted ONLY when its input becomes unconstructible at
	//   this layer (rejected at selector compile time), and the deleting
	//   commit MUST re-assert that input's rejection in
	//   TestArchiveFilterRejectsInvalidPatterns in this package;
	// - row names and row order are otherwise stable. A name describes its
	//   row's INPUT, never the outcome, in the whitelist/blacklist spelling the
	//   inputs had when the table was frozen; the spelling is kept deliberately
	//   so a semantics change still reads as an expectation diff and not as a
	//   rename.
	//
	// The one_of_each archives contain:
	// - an empty file
	// - a valid file with a size of 175 kB
	// - a valid file with a size of 1.2 MB
	// - a valid file with a size of 2.3 MB
	// - a binary file with a size of 1 MB
	// - a valid file to whitelist
	// - a valid file to blacklist
	//
	// The filter_semantics archives (built by
	// testdata/archives/gen_filter_semantics.go) contain four small text
	// members, chosen so that a literal reading and a regex reading of the
	// same pattern disagree:
	// - app.log             regex `.*\.log$` selects it, the literal never does
	// - temp.*.txt          the only member holding the literal text `temp.*`
	// - temporary_notes.txt regex `temp.*` selects it, the literal never does
	// - UPPER_CASE.TXT      uppercase name, reached by a lowercase pattern only
	//                       because the rule matches with ignoreCase
	baseTests := []struct {
		name          string
		baseFile      string
		maxLen        int
		filter        selector.Spec
		unpackedFiles []string
	}{
		{"Archive with maxSize filter", "one_of_each", 2 * 1024 * 1024, memberSpec(nil, nil), []string{"large_valid.txt", "very_large_but_valid.txt", "black/to_be_blacklisted.blst", "white/to_be_whitelisted.wlst"}},
		{"Archive with smaller maxSize filter", "one_of_each", 0.5 * 1024 * 1024, memberSpec(nil, nil), []string{"large_valid.txt", "black/to_be_blacklisted.blst", "white/to_be_whitelisted.wlst"}},
		{"Archive with whitelist filter", "one_of_each", 2 * 1024 * 1024, memberSpec([]string{`\.wlst`}, nil), []string{"white/to_be_whitelisted.wlst"}},
		{"Archive with whitelist filter 2", "one_of_each", 2 * 1024 * 1024, memberSpec([]string{"to_be_whitelisted"}, nil), []string{"white/to_be_whitelisted.wlst"}},
		{"Archive with blacklist filter", "one_of_each", 2 * 1024 * 1024, memberSpec(nil, []string{`\.blst`}), []string{"large_valid.txt", "very_large_but_valid.txt", "white/to_be_whitelisted.wlst"}},
		{"Archive with overlapping filters", "one_of_each", 0.5 * 1024 * 1024, memberSpec(nil, []string{`\.blst`}), []string{"large_valid.txt", "white/to_be_whitelisted.wlst"}},
		{"Archive with overlapping filters 2", "one_of_each", 10, memberSpec([]string{"wlst"}, nil), []string{}},
		// The quoted metacharacters still match themselves: no member path holds
		// the text `.*\.log$`, so the whitelist admits nothing and the blacklist
		// excludes nothing - app.log survives both. A raw (unquoted) `.*\.log$`
		// is a regex that would select app.log - the expressiveness the legacy
		// translation withheld.
		{"Archive with whitelist `.*\\.log$`", "filter_semantics", 2 * 1024 * 1024, memberSpec([]string{`\.\*\\\.log\$`}, nil), []string{}},
		{"Archive with blacklist `.*\\.log$`", "filter_semantics", 2 * 1024 * 1024, memberSpec(nil, []string{`\.\*\\\.log\$`}), []string{"app.log", "temp.*.txt", "temporary_notes.txt", "UPPER_CASE.TXT"}},
		// `temp.*` reaches only the member that spells it out, never the one a
		// regex would reach through `.*`.
		{"Archive with whitelist `temp.*`", "filter_semantics", 2 * 1024 * 1024, memberSpec([]string{`temp\.\*`}, nil), []string{"temp.*.txt"}},
		// A glob-shaped entry survives as a literal: quoting turns what RE2 would
		// reject as a regex into a pattern matching the text `*.txt`.
		{"Archive with whitelist `*.txt`", "filter_semantics", 2 * 1024 * 1024, memberSpec([]string{`\*\.txt`}, nil), []string{"temp.*.txt"}},
		// Matching is case-insensitive in both directions.
		{"Archive with lowercase whitelist `upper_case`", "filter_semantics", 2 * 1024 * 1024, memberSpec([]string{"upper_case"}, nil), []string{"UPPER_CASE.TXT"}},
		{"Archive with uppercase blacklist `TEMPORARY`", "filter_semantics", 2 * 1024 * 1024, memberSpec(nil, []string{"TEMPORARY"}), []string{"app.log", "temp.*.txt", "UPPER_CASE.TXT"}},
		// Matching runs over the full member path, so a directory component is
		// a usable pattern - a basename-only subject would admit nothing here.
		{"Archive with whitelist on a directory component", "one_of_each", 2 * 1024 * 1024, memberSpec([]string{"white/"}, nil), []string{"white/to_be_whitelisted.wlst"}},
		// Both lists set: include AND NOT exclude, so `temp` restricts what the
		// blacklist leaves - UPPER_CASE.TXT is no longer admitted, the one
		// verdict the whitelist-suppression rule used to produce. Include and
		// exclude are legal together on the [[rule]] surface.
		{"Archive with whitelist `temp` and blacklist `.log`", "filter_semantics", 2 * 1024 * 1024, memberSpec([]string{"temp"}, []string{`\.log`}), []string{"temp.*.txt", "temporary_notes.txt"}},
	}

	var tests []struct {
		name          string
		filepath      string
		maxLen        int
		filter        selector.Spec
		unpackedFiles []string
	}

	formats := []string{".zip", ".7z", ".tar"}

	for _, base := range baseTests {
		for _, ext := range formats {
			tests = append(tests, struct {
				name          string
				filepath      string
				maxLen        int
				filter        selector.Spec
				unpackedFiles []string
			}{
				name:          fmt.Sprintf("%s (%s)", base.name, ext),
				filepath:      fmt.Sprintf("../../testdata/archives/%s%s", base.baseFile, ext),
				maxLen:        base.maxLen,
				filter:        base.filter,
				unpackedFiles: base.unpackedFiles,
			})
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parts := strings.Split(test.filepath, "/")
			filename := parts[len(parts)-1]
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: int64(test.maxLen), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, mustMemberFilter(t, test.filter))
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

// TestArchiveFilterRejectsInvalidPatterns pins the boundary the frozen table
// stops at: the iterator's filter is constructible only through
// selector.Compile, which refuses what the old literal matcher swallowed - an
// empty entry (inert then) and an uncompilable regex (a plain literal then).
// The transitional translation dropped an empty entry before compiling, and the
// frozen table held rows for the inputs that reached it that way; the
// translation is gone, so this rejection is the whole story for them.
func TestArchiveFilterRejectsInvalidPatterns(t *testing.T) {
	for _, spec := range []selector.Spec{
		{Rule: "member", Subject: "path", IgnoreCase: true, Include: []string{""}},
		{Rule: "member", Subject: "path", IgnoreCase: true, Exclude: []string{""}},
		{Rule: "member", Subject: "path", IgnoreCase: true, Include: []string{"temp", ""}},
		// The receipt for the deleted "empty and non-empty whitelist entries"
		// row: the sibling above covers the same class, this one covers that
		// row's own input.
		{Rule: "member", Subject: "path", IgnoreCase: true, Include: []string{"", `temp\.\*`}},
	} {
		_, err := selector.Compile(spec)
		assert.ErrorIs(t, err, selector.ErrEmptyPattern, "%+v must not compile into a member filter", spec)
	}

	// The uncompilable regex is refused by a different error.
	_, err := selector.Compile(selector.Spec{Rule: "member", Subject: "path", IgnoreCase: true, Include: []string{"("}})
	assert.Error(t, err, "an uncompilable regex must not compile into a member filter")
}

// TestAdmitMemberEmptyName pins the declared flip for the nameless member a
// crafted archive can hold: the old matcher short-circuited an empty subject as
// "matches", so a blacklist rejected it and a whitelist admitted it. Patterns
// now match it like any other subject, inverting both verdicts.
func TestAdmitMemberEmptyName(t *testing.T) {
	include, err := selector.Compile(selector.Spec{Rule: "member", Subject: "path", IgnoreCase: true, Include: []string{"temp"}})
	require.NoError(t, err)
	exclude, err := selector.Compile(selector.Spec{Rule: "member", Subject: "path", IgnoreCase: true, Exclude: []string{"temp"}})
	require.NoError(t, err)

	limits := ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 1024, MaxMemberCount: 10}
	assert.False(t, InitArchiveIterator(context.Background(), "x.zip", "x.zip", limits, &include).admitMember(""),
		"a whitelist admitted the nameless member before, and rejects it now")
	assert.True(t, InitArchiveIterator(context.Background(), "x.zip", "x.zip", limits, &exclude).admitMember(""),
		"a blacklist rejected the nameless member before, and admits it now")
	assert.True(t, InitArchiveIterator(context.Background(), "x.zip", "x.zip", limits, nil).admitMember(""),
		"an unfiltered iterator admits it either way")
}

// TestArchiveMemberFilterSubject: the iterator matches the subject the filter
// declares - the full member path, or its base name for subject "name".
func TestArchiveMemberFilterSubject(t *testing.T) {
	archive := buildMemberZip(t, map[string][]byte{
		"sub/notes.txt": []byte("member text\n"),
		"sub/data.csv":  []byte("a,b\n"),
	})
	members := func(t *testing.T, spec selector.Spec) []string {
		t.Helper()
		sel, err := selector.Compile(spec)
		assert.NoError(t, err)
		nfi := InitArchiveIterator(context.Background(), archive, "members.zip",
			ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 10}, &sel)
		defer nfi.Close()
		var names []string
		for nfi.HasFilesToUnpack() && nfi.HasNext() {
			nfi.Next()
			name, _, _ := nfi.UnpackedFile()
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}

	assert.Equal(t, []string{"sub/data.csv", "sub/notes.txt"},
		members(t, selector.Spec{Rule: "member", Subject: "path", Include: []string{"sub/"}}),
		"subject path sees the directory component")
	assert.Empty(t, members(t, selector.Spec{Rule: "member", Subject: "name", Include: []string{"sub/"}}),
		"subject name never sees the directory component")
	assert.Equal(t, []string{"sub/notes.txt"},
		members(t, selector.Spec{Rule: "member", Subject: "name", Include: []string{"notes"}}),
		"subject name matches the base name")
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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
			nfi := InitArchiveIterator(context.Background(), test.filepath, filename, ArchiveLimits{MaxMemberSize: 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
			nfi := InitArchiveIterator(context.Background(), path, filename, ArchiveLimits{MaxMemberSize: int64(0.5 * 1024 * 1024), MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
	it := InitArchiveIterator(context.Background(), "x", "x.zip", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
	t.Run("much shorter than declared releases the backing array", func(t *testing.T) {
		isText, content, overrun, err := it.sniffThenRead(strings.NewReader(text(600)), 1024*1024)
		assert.NoError(t, err)
		assert.True(t, isText)
		assert.False(t, overrun)
		assert.Equal(t, text(600), string(content))
		assert.Equal(t, len(content), cap(content), "declared-size backing must be released")
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

	nfi := InitArchiveIterator(context.Background(), path, "bomb.zip", ArchiveLimits{MaxMemberSize: 2 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
	nfi := InitArchiveIterator(context.Background(), path, "one_of_each.7z", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 1000}, nil)
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
	ok := InitArchiveIterator(context.Background(), path, "one_of_each.7z", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 2 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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

	nfi := InitArchiveIterator(context.Background(), path, "ok.tar.gz", ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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

	nfi := InitArchiveIterator(context.Background(), path, "big.tar.gz", ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 1000}, nil)
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

	nfi := InitArchiveIterator(context.Background(), path, "huge.tar.gz", ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 1024 * 1024, MaxMemberCount: 1000}, nil)
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

func TestTarGzMemberNamesMatchFileList(t *testing.T) {
	archive := structs.File{Path: "../../testdata/archives/test.tar.gz", Name: "test.tar.gz", DisplayName: "test.tar.gz", Suffix: ".gz"}
	want, truncated, err := ReadArchiveFileList(archive, 1000, 100*1024*1024)
	require.NoError(t, err)
	require.False(t, truncated)

	collector := structs.NewArchiveNameCollector(1000)
	nfi := InitArchiveIterator(context.Background(), archive.Path, archive.Name,
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	nfi.MemberNames = collector
	assert.True(t, nfi.HasFilesToUnpack())
	for nfi.HasNext() {
		nfi.Next()
	}

	names, ok := collector.Result()
	assert.True(t, ok, "a walk that reached the end of the archive must yield the member list")
	// Every header, so the directory and the zero-size member are in there too -
	// the name checks must see exactly what the file-list walk shows them.
	assert.Equal(t, want, names)
}

func TestTarGzMemberNamesCapTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capped.tar.gz")
	text := []byte(strings.Repeat("line\n", 200))
	var members []struct {
		name    string
		content []byte
	}
	for i := 0; i < 8; i++ {
		members = append(members, struct {
			name    string
			content []byte
		}{fmt.Sprintf("m%d.txt", i), text})
	}
	writeTarGzFixture(t, path, members)

	collector := structs.NewArchiveNameCollector(3)
	nfi := InitArchiveIterator(context.Background(), path, "capped.tar.gz",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	nfi.MemberNames = collector
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		count++
	}
	assert.Equal(t, 8, count, "the content scan keeps every member the cap has nothing to do with")

	names, ok := collector.Result()
	assert.False(t, ok, "more members than the cap must not yield a member list")
	assert.Nil(t, names)
}

func TestTarGzMemberNamesTruncatedStreamYieldsNoList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cut.tar.gz")
	text := []byte(strings.Repeat("line\n", 200))
	writeTarGzFixture(t, path, []struct {
		name    string
		content []byte
	}{{"a.txt", text}, {"b.txt", text}, {"c.txt", text}})
	// A truncated upload: the compressed stream stops mid-archive, so the walk
	// runs out of data instead of reaching the end-of-archive marker.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(path, info.Size()-40))

	collector := structs.NewArchiveNameCollector(1000)
	nfi := InitArchiveIterator(context.Background(), path, "cut.tar.gz",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	nfi.MemberNames = collector
	assert.True(t, nfi.HasFilesToUnpack(), "the members before the cut are still readable")
	for nfi.HasNext() {
		nfi.Next()
	}

	_, ok := collector.Result()
	assert.False(t, ok, "a stream that ends short of the archive leaves a partial list, not a member list")
}

func TestTarGzMemberNamesCapBoundsHeaderFlood(t *testing.T) {
	// The worst shape: a small tar.gz holding hundreds of thousands of zero-size
	// members, all of them inside the walk budget. Counting only content-scan
	// candidates would never reach the cap here (there are none), so the walk
	// would run to a clean end and hand out a list as wide as the archive.
	path := filepath.Join(t.TempDir(), "flood.tar.gz")
	f, err := os.Create(path)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	header := tar.Header{Mode: 0o600, Size: 0, Typeflag: tar.TypeReg}
	for i := 0; i < 200_000; i++ {
		header.Name = fmt.Sprintf("m%07d", i)
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatalf("writing member %d: %v", i, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	collector := structs.NewArchiveNameCollector(1000)
	nfi := InitArchiveIterator(context.Background(), path, "flood.tar.gz",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 200 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	nfi.MemberNames = collector
	assert.False(t, nfi.HasFilesToUnpack(), "zero-size members are never content-scan candidates")
	assert.Empty(t, nfi.SkipMessages(), "the walk must end on the archive, not on a budget")

	names, ok := collector.Result()
	assert.False(t, ok, "a header flood past the cap must not yield a member list")
	assert.Nil(t, names, "the collected names must be released, not held for the rest of the walk")
}

// buildMemberZip writes a zip archive with the given members to a temp path.
func buildMemberZip(t *testing.T, members map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range members {
		w, err := zw.Create(name)
		assert.NoError(t, err)
		_, err = w.Write(data)
		assert.NoError(t, err)
	}
	assert.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "members.zip")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	return path
}

func TestOOXMLMemberExtraction(t *testing.T) {
	xlsxData := buildAmplifiedXLSX(t)
	docxData, err := os.ReadFile("../../testdata/test.docx")
	assert.NoError(t, err)
	path := buildMemberZip(t, map[string][]byte{
		"report.xlsx": xlsxData,
		"notes.docx":  docxData,
		"plain.txt":   []byte("ordinary text content\n"),
	})

	nfi := InitArchiveIterator(context.Background(), path, "members.zip",
		ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	got := map[string][]byte{}
	for nfi.HasNext() {
		nfi.Next()
		name, content, _ := nfi.UnpackedFile()
		got[name] = content
	}
	assert.Len(t, got, 3)
	assert.Contains(t, string(got["report.xlsx"]), "AAAA", "xlsx member text must be extracted")
	assert.Contains(t, string(got["notes.docx"]), "PAGE 1", "docx member text must be extracted")
	assert.Equal(t, "ordinary text content\n", string(got["plain.txt"]))
}

func TestOOXMLMemberTruncationAndCharge(t *testing.T) {
	// Amplified container: small zip entries, ~400 KB extracted. Member cap
	// 16 KB -> truncated ack, partial text scanned, charge stays bounded.
	path := buildMemberZip(t, map[string][]byte{"big.xlsx": buildAmplifiedXLSX(t)})
	nfi := InitArchiveIterator(context.Background(), path, "members.zip",
		ArchiveLimits{MaxMemberSize: 16 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		_, content, _ := nfi.UnpackedFile()
		assert.NotEmpty(t, content)
		count++
	}
	assert.Equal(t, 1, count)
	foundStop := false
	for _, m := range nfi.SkipMessages() {
		if strings.Contains(m.Content, "Stopped content scan of archive member") {
			foundStop = true
		}
	}
	assert.True(t, foundStop, "expected truncation ack, got %+v", nfi.SkipMessages())
	assert.LessOrEqual(t, nfi.totalMemoryUsed, int64(64*1024), "charge must reflect the capped extraction")
}

func TestOOXMLMemberGateReject(t *testing.T) {
	// Container member whose inner index declares 1 TB: gate ack, member
	// skipped, iteration continues to the scannable member behind it.
	var raw bytes.Buffer
	zw := zip.NewWriter(&raw)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "xl/worksheets/sheet1.xml",
		Method:             zip.Deflate,
		UncompressedSize64: 1 << 40,
		CompressedSize64:   8,
	})
	assert.NoError(t, err)
	_, err = w.Write([]byte{0x07, 0xff, 0xff, 0xff, 0, 0, 0, 0})
	assert.NoError(t, err)
	assert.NoError(t, zw.Close())

	path := buildMemberZip(t, map[string][]byte{
		"bomb.xlsx": raw.Bytes(),
		"safe.txt":  []byte("still scanned\n"),
	})
	nfi := InitArchiveIterator(context.Background(), path, "members.zip",
		ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	var names []string
	for nfi.HasNext() {
		nfi.Next()
		name, _, _ := nfi.UnpackedFile()
		names = append(names, name)
	}
	assert.Equal(t, []string{"safe.txt"}, names)
	foundGate := false
	for _, m := range nfi.SkipMessages() {
		if strings.Contains(m.Content, "container declares more data") {
			foundGate = true
		}
	}
	assert.True(t, foundGate, "expected gate ack, got %+v", nfi.SkipMessages())
}

func TestOOXMLMemberMisnamedTextFallback(t *testing.T) {
	path := buildMemberZip(t, map[string][]byte{"data.xlsx": []byte("csv,misnamed,as,xlsx\nplain,text\n")})
	nfi := InitArchiveIterator(context.Background(), path, "members.zip",
		ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	nfi.Next()
	name, content, _ := nfi.UnpackedFile()
	assert.Equal(t, "data.xlsx", name)
	assert.Equal(t, "csv,misnamed,as,xlsx\nplain,text\n", string(content), "misnamed text member must be scanned raw")
}

func countMemberCountAcks(msgs []structs.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, "maximum archive member count") {
			n++
		}
	}
	return n
}

func TestMemberCountLimitZip7z(t *testing.T) {
	// zip/7z pre-count over the index: an over-limit archive is rejected with
	// ZERO member reads; at the limit everything scans with no ack.
	for _, ext := range []string{".zip", ".7z"} {
		t.Run("over limit "+ext, func(t *testing.T) {
			nfi := InitArchiveIterator(context.Background(), "../../testdata/archives/ten_valid_files"+ext, "ten_valid_files"+ext,
				ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 5}, nil)
			assert.False(t, nfi.HasFilesToUnpack())
			assert.Equal(t, 1, countMemberCountAcks(nfi.SkipMessages()))
			assert.Equal(t, int64(0), nfi.totalMemoryUsed, "over-limit archive must have zero member reads")
			assert.Equal(t, 0, nfi.processedFileCount)
		})
		t.Run("at limit "+ext, func(t *testing.T) {
			nfi := InitArchiveIterator(context.Background(), "../../testdata/archives/ten_valid_files"+ext, "ten_valid_files"+ext,
				ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 10}, nil)
			assert.True(t, nfi.HasFilesToUnpack())
			count := 0
			for nfi.HasNext() {
				nfi.Next()
				count++
			}
			assert.Equal(t, 10, count)
			assert.Equal(t, 0, countMemberCountAcks(nfi.SkipMessages()))
		})
	}
}

func TestMemberCountLimitFilteredMembersDoNotCount(t *testing.T) {
	// Name-filtered members are not candidates: 10 members, 6 blacklisted,
	// limit 5 -> passes.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("m%d.txt", i)
		if i >= 4 {
			name = fmt.Sprintf("m%d.blst", i)
		}
		w, err := zw.Create(name)
		assert.NoError(t, err)
		_, err = w.Write([]byte("plain text content\n"))
		assert.NoError(t, err)
	}
	assert.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "filtered.zip")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	nfi := InitArchiveIterator(context.Background(), path, "filtered.zip",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 5}, mustMemberFilter(t, memberSpec(nil, []string{`\.blst`})))
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		count++
	}
	assert.Equal(t, 4, count)
	assert.Equal(t, 0, countMemberCountAcks(nfi.SkipMessages()))
}

func TestMemberCountLimitTarInline(t *testing.T) {
	// tar family counts inline: members yielded before the limit stay
	// scanned, then one archive-level ack and the iteration stops.
	nfi := InitArchiveIterator(context.Background(), "../../testdata/archives/ten_valid_files.tar", "ten_valid_files.tar",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 5}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		count++
	}
	assert.Equal(t, 5, count, "members before the limit stay scanned")
	assert.Equal(t, 1, countMemberCountAcks(nfi.SkipMessages()))
}

func TestMemberCountLimitTarGz(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.tar.gz")
	text := []byte(strings.Repeat("line\n", 200))
	var members []struct {
		name    string
		content []byte
	}
	for i := 0; i < 8; i++ {
		members = append(members, struct {
			name    string
			content []byte
		}{fmt.Sprintf("m%d.txt", i), text})
	}
	writeTarGzFixture(t, path, members)

	nfi := InitArchiveIterator(context.Background(), path, "many.tar.gz",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 3}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		count++
	}
	assert.Equal(t, 3, count)
	assert.Equal(t, 1, countMemberCountAcks(nfi.SkipMessages()))
}

func TestMemberCountLimitFolderHeavyZip(t *testing.T) {
	// Directory entries carry size 0 and never count as candidates.
	nfi := InitArchiveIterator(context.Background(), "../../testdata/archives/only_folders.zip", "only_folders.zip",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1}, nil)
	assert.False(t, nfi.HasFilesToUnpack())
	assert.Equal(t, 0, countMemberCountAcks(nfi.SkipMessages()), "folder-only archives must not trip the member count")
}

func TestSkipAckPrecedence(t *testing.T) {
	// filter (silent) -> size -> memory: labels must match the actual reason,
	// independent of how much budget earlier members consumed; name-filtered
	// members get no acknowledgements at all.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	text := func(n int) []byte { return bytes.Repeat([]byte("a"), n) }
	for _, m := range []struct {
		name string
		size int
	}{
		{"a.txt", 1024},     // buffered (consumes budget)
		{"skip.blst", 4096}, // blacklisted: silent, despite being oversized
		{"big.txt", 4096},   // over member size: size ack, NOT memory
		{"c.txt", 2048},     // within member size, over remaining budget: memory ack
	} {
		w, err := zw.Create(m.name)
		assert.NoError(t, err)
		_, err = w.Write(text(m.size))
		assert.NoError(t, err)
	}
	assert.NoError(t, zw.Close())
	path := filepath.Join(t.TempDir(), "prec.zip")
	assert.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	nfi := InitArchiveIterator(context.Background(), path, "prec.zip",
		ArchiveLimits{MaxMemberSize: 2048, MaxTotalMemory: 2560, MaxMemberCount: 1000}, mustMemberFilter(t, memberSpec(nil, []string{`\.blst`})))
	assert.True(t, nfi.HasFilesToUnpack())
	var yielded []string
	for nfi.HasNext() {
		nfi.Next()
		name, _, _ := nfi.UnpackedFile()
		yielded = append(yielded, name)
	}
	assert.Equal(t, []string{"a.txt"}, yielded)

	skips := nfi.SkipMessages()
	assert.Len(t, skips, 2)
	byName := map[string]string{}
	for _, m := range skips {
		src, ok := m.Source.(structs.File)
		assert.True(t, ok)
		byName[src.Name] = m.Content
	}
	assert.Contains(t, byName["big.txt"], "maximum archive member size", "oversized member must be size-labeled even with budget consumed")
	assert.Contains(t, byName["c.txt"], "total archive memory limit")
	assert.NotContains(t, byName, "skip.blst", "name-filtered members must not be acknowledged")
}

func TestTruncatedTarMemberYieldsTruncatedContent(t *testing.T) {
	// Decided in the hardening plan (H3): a member cut off by archive
	// truncation is scanned with the content that IS there, instead of being
	// silently skipped. Iteration still ends right after (tar errors are sticky).
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	full := []byte(strings.Repeat("text line\n", 400)) // 4000 bytes declared
	assert.NoError(t, tw.WriteHeader(&tar.Header{Name: "cut.txt", Mode: 0o600, Size: int64(len(full)), Typeflag: tar.TypeReg}))
	_, err := tw.Write(full)
	assert.NoError(t, err)
	assert.NoError(t, tw.Close())

	path := filepath.Join(t.TempDir(), "cut.tar")
	// Truncate mid-member: header block (512) + 2048 content bytes.
	assert.NoError(t, os.WriteFile(path, buf.Bytes()[:512+2048], 0o600))

	nfi := InitArchiveIterator(context.Background(), path, "cut.tar",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	assert.True(t, nfi.HasFilesToUnpack())
	count := 0
	for nfi.HasNext() {
		nfi.Next()
		name, content, _ := nfi.UnpackedFile()
		assert.Equal(t, "cut.txt", name)
		assert.Equal(t, full[:2048], content, "truncated member must yield exactly the bytes present")
		count++
	}
	assert.Equal(t, 1, count)
}

func TestIteratorCloseEarly(t *testing.T) {
	nfi := InitArchiveIterator(context.Background(), "../../testdata/archives/ten_valid_files.zip", "ten_valid_files.zip",
		ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
	assert.True(t, nfi.HasFilesToUnpack())

	nfi.Close()
	assert.Nil(t, nfi.zipReader, "Close must release the archive handle")
	assert.False(t, nfi.HasNext(), "Close must end iteration")
	assert.False(t, nfi.Next())
	nfi.Close() // idempotent
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

			nfi := InitArchiveIterator(context.Background(), path, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: 100 * 1024 * 1024, MaxMemberCount: 1000}, nil)
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
			tight := InitArchiveIterator(context.Background(), path, filename, ArchiveLimits{MaxMemberSize: 1024 * 1024, MaxTotalMemory: contentSum, MaxMemberCount: 1000}, nil)
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
			nfi := InitArchiveIterator(context.Background(), path, filename, ArchiveLimits{MaxMemberSize: 10 * 1024 * 1024, MaxTotalMemory: 1536 * 1024, MaxMemberCount: 1000}, nil)
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
