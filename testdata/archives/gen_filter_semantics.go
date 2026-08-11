//go:build ignore

// Generator for the filter_semantics.{zip,tar,7z} fixtures used by
// TestFiltersDuringArchiveIteration (pkg/readers/archive_iterator_test.go).
//
// The four members are chosen so that a literal reading and a regex reading of
// the same pattern disagree:
//
//	app.log             regex `.*\.log$` selects it, a literal never does
//	temp.*.txt          the only member holding the literal text `temp.*`
//	temporary_notes.txt regex `temp.*` selects it, the literal never does
//	UPPER_CASE.TXT      uppercase name, for case-sensitivity coverage
//
// Usage (from this directory):
//
//	go run gen_filter_semantics.go
//
// It rewrites filter_semantics.zip and filter_semantics.tar in place, stages
// the four members in a temp directory and prints the p7zip command that
// produces filter_semantics.7z from them - Go has no 7z writer
// (github.com/bodgit/sevenzip is read-only), so that step stays manual. The
// -spd switch is required: without it p7zip treats the member name temp.*.txt
// as a wildcard.
package main

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type member struct {
	name    string
	content string
}

var members = []member{
	{"app.log", "2026-08-11 12:00:00 INFO log member: regex .*\\.log$ only\n"},
	{"temp.*.txt", "member whose name literally contains the text temp.*\n"},
	{"temporary_notes.txt", "member that only a regex temp.* would select\n"},
	{"UPPER_CASE.TXT", "uppercase member name for case sensitivity\n"},
}

func main() {
	writeZip("filter_semantics.zip")
	writeTar("filter_semantics.tar")

	stage, err := os.MkdirTemp("", "filter_semantics")
	must(err)
	for _, m := range members {
		must(os.WriteFile(filepath.Join(stage, m.name), []byte(m.content), 0o644))
	}
	out, err := filepath.Abs("filter_semantics.7z")
	must(err)

	fmt.Println("wrote filter_semantics.zip and filter_semantics.tar")
	fmt.Printf("now run, from %s:\n", stage)
	fmt.Printf("  rm -f %s && 7z a -spd %s", out, out)
	for _, m := range members {
		fmt.Printf(" '%s'", m.name)
	}
	fmt.Println()
}

func writeZip(path string) {
	f, err := os.Create(path)
	must(err)
	zw := zip.NewWriter(f)
	for _, m := range members {
		w, err := zw.Create(m.name)
		must(err)
		_, err = w.Write([]byte(m.content))
		must(err)
	}
	must(zw.Close())
	must(f.Close())
}

func writeTar(path string) {
	f, err := os.Create(path)
	must(err)
	tw := tar.NewWriter(f)
	for _, m := range members {
		must(tw.WriteHeader(&tar.Header{
			Name:     m.name,
			Mode:     0o644,
			Size:     int64(len(m.content)),
			Typeflag: tar.TypeReg,
		}))
		_, err = tw.Write([]byte(m.content))
		must(err)
	}
	must(tw.Close())
	must(f.Close())
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
