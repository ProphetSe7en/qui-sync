package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteChangelogNoteFreshFile covers the "no prior CHANGELOG_NOTES.md"
// path — the helper must create the file with a single section.
func TestWriteChangelogNoteFreshFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteChangelogNote(dir, "2026-04-19", "added HDBits to Tier 1"); err != nil {
		t.Fatalf("WriteChangelogNote: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "CHANGELOG_NOTES.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := "## 2026-04-19\n\nadded HDBits to Tier 1\n"
	if string(got) != want {
		t.Errorf("fresh write:\n got: %q\nwant: %q", got, want)
	}
}

// TestWriteChangelogNoteReplacesExisting confirms that writing a note
// for a date that already has a section REPLACES the old content rather
// than duplicating it.
func TestWriteChangelogNoteReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	initial := "## 2026-04-19\n\nfirst version\n"
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG_NOTES.md"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteChangelogNote(dir, "2026-04-19", "second version"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "CHANGELOG_NOTES.md"))
	if strings.Contains(string(got), "first version") {
		t.Errorf("old content not replaced:\n%s", got)
	}
	if !strings.Contains(string(got), "second version") {
		t.Errorf("new content missing:\n%s", got)
	}
}

// TestWriteChangelogNotePrependsNewest confirms that a new date section
// goes to the TOP of the file, matching CHANGELOG.md's ordering.
func TestWriteChangelogNotePrependsNewest(t *testing.T) {
	dir := t.TempDir()
	older := "## 2026-04-18\n\nolder note\n"
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG_NOTES.md"), []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteChangelogNote(dir, "2026-04-19", "newer note"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "CHANGELOG_NOTES.md"))
	newerIdx := strings.Index(string(got), "## 2026-04-19")
	olderIdx := strings.Index(string(got), "## 2026-04-18")
	if newerIdx < 0 || olderIdx < 0 {
		t.Fatalf("both sections should be present: %s", got)
	}
	if newerIdx > olderIdx {
		t.Errorf("newer section should come first:\n%s", got)
	}
}

// TestWriteChangelogNoteEmptyRemovesSection confirms that passing an
// empty note removes the date's section, leaving the rest of the file
// intact.
func TestWriteChangelogNoteEmptyRemovesSection(t *testing.T) {
	dir := t.TempDir()
	initial := "## 2026-04-19\n\nto remove\n\n## 2026-04-18\n\nto keep\n"
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG_NOTES.md"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteChangelogNote(dir, "2026-04-19", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "CHANGELOG_NOTES.md"))
	if strings.Contains(string(got), "to remove") {
		t.Errorf("target section not removed:\n%s", got)
	}
	if !strings.Contains(string(got), "to keep") {
		t.Errorf("other section should remain:\n%s", got)
	}
}

// TestReadManualNotesRoundTrip confirms WriteChangelogNote + readManualNotes
// play together — whatever we write comes back on read.
func TestReadManualNotesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	note := "Updated Tier 1 trackers after HDBits joined.\n\nSee forum thread #123 for context."
	if err := WriteChangelogNote(dir, "2026-04-19", note); err != nil {
		t.Fatal(err)
	}
	got := readManualNotes(dir, "2026-04-19")
	if got != note {
		t.Errorf("round-trip mismatch:\n got: %q\nwant: %q", got, note)
	}
}

// TestWriteChangelogNoteEmptyOnEmptyFileLeavesNothing ensures the "clear
// the only section" path removes the file entirely rather than leaving
// an empty husk.
func TestWriteChangelogNoteEmptyOnEmptyFileLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := WriteChangelogNote(dir, "2026-04-19", "something"); err != nil {
		t.Fatal(err)
	}
	if err := WriteChangelogNote(dir, "2026-04-19", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "CHANGELOG_NOTES.md")); !os.IsNotExist(err) {
		t.Errorf("file should be removed after clearing its only section, err=%v", err)
	}
}
