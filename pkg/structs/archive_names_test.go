package structs

import (
	"reflect"
	"testing"
)

func TestArchiveNameCollectorSecondFillRefused(t *testing.T) {
	c := NewArchiveNameCollector(10)
	if !c.BeginFill() {
		t.Fatal("the first BeginFill must claim the collector")
	}
	if c.BeginFill() {
		t.Error("a second BeginFill must be refused: only one walk may fill a collector")
	}
	c.Note(File{Name: "test/file1.txt"})
	c.Finish()

	if _, ok := c.Result(); ok {
		t.Error("a refused fill must not be usable, however cleanly the first walk ended")
	}
}

func TestArchiveNameCollectorUnfinishedFillUnusable(t *testing.T) {
	// Not reaching Finish is the whole signal a walk that stopped short gives:
	// what it collected stays a partial list nobody may read as the member list.
	c := NewArchiveNameCollector(10)
	c.BeginFill()
	c.Note(File{Name: "test/file1.txt"})

	if _, ok := c.Result(); ok {
		t.Error("a fill that never ended must not be usable")
	}
}

func TestArchiveNameCollectorAtCapKeepsNames(t *testing.T) {
	// The cap is what the archive may hold, not one less: an archive with
	// exactly max members keeps its list.
	c := NewArchiveNameCollector(3)
	c.BeginFill()
	noted := []File{{Name: "test/"}, {Name: "test/file2"}, {Name: "test/file1.txt", Size: 6}}
	for _, f := range noted {
		c.Note(f)
	}
	c.Finish()

	names, ok := c.Result()
	if !ok {
		t.Error("exactly max members is within the cap: the fill must stay usable")
	}
	if !reflect.DeepEqual(names, noted) {
		t.Errorf("Result() names = %v, want %v", names, noted)
	}
}

func TestArchiveNameCollectorCapReleasesNames(t *testing.T) {
	// Every Note counts, so the third one busts a cap of two and drops what was
	// collected instead of holding a partial list for the rest of the walk.
	c := NewArchiveNameCollector(2)
	c.BeginFill()
	c.Note(File{Name: "test/"})
	c.Note(File{Name: "test/file2"})
	c.Note(File{Name: "test/file1.txt"})
	c.Finish()

	names, ok := c.Result()
	if ok {
		t.Error("a fill past the cap must not be usable")
	}
	if names != nil {
		t.Errorf("Result() names = %v, want the collected names released", names)
	}
}
