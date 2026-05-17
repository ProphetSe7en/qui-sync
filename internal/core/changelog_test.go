package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readChangelog(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG.md: %v", err)
	}
	return string(data)
}

// TestAppendChangelogPerRuleComment covers the per-rule comment
// suffix added in v0.4.1-dev — each diff bullet with a non-empty
// Comment renders an italic "— *...*" tail.
func TestAppendChangelogPerRuleComment(t *testing.T) {
	dir := t.TempDir()
	diff := &ExportDiff{
		Renamed: []DiffEntry{{
			Slug: "foo", Category: "movies", OldName: "Foo", Name: "Foo (HD)",
			Comment: "added HD suffix",
		}},
		Added: []DiffEntry{
			{Slug: "bar", Category: "movies", Name: "Bar"}, // no comment
		},
	}
	when := time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)
	if err := AppendChangelog(dir, diff, when, ""); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	if !strings.Contains(got, `"Foo" → "Foo (HD)" — *added HD suffix*`) {
		t.Errorf("rename comment suffix missing:\n%s", got)
	}
	// Entry without a comment must NOT get a trailing em-dash.
	if strings.Contains(got, "movies) — Bar — *") {
		t.Errorf("empty comment should not render suffix:\n%s", got)
	}
}

// TestAppendChangelogCommentSanitisesNewlines confirms multi-line
// comments are flattened so the markdown bullet structure survives.
func TestAppendChangelogCommentSanitisesNewlines(t *testing.T) {
	dir := t.TempDir()
	diff := &ExportDiff{
		Added: []DiffEntry{{
			Slug: "a", Category: "movies", Name: "Alpha",
			Comment: "first line\nsecond line\r\nthird",
		}},
	}
	if err := AppendChangelog(dir, diff, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	if !strings.Contains(got, "*first line second line third*") {
		t.Errorf("newlines not collapsed:\n%s", got)
	}
}

// TestAppendChangelogFreshFile covers the "no prior CHANGELOG.md" path
// — the helper must create the file with header + a single section.
func TestAppendChangelogFreshFile(t *testing.T) {
	dir := t.TempDir()
	diff := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	if err := AppendChangelog(dir, diff, when, "added HDBits to Tier 1"); err != nil {
		t.Fatalf("AppendChangelog: %v", err)
	}
	got := readChangelog(t, dir)
	if !strings.Contains(got, "## 2026-04-19") {
		t.Errorf("section heading missing:\n%s", got)
	}
	if !strings.Contains(got, "added HDBits to Tier 1") {
		t.Errorf("note missing:\n%s", got)
	}
	if !strings.Contains(got, "### Added") {
		t.Errorf("group heading missing:\n%s", got)
	}
}

// TestAppendChangelogNoNote — empty note string must render cleanly
// without an empty paragraph.
func TestAppendChangelogNoNote(t *testing.T) {
	dir := t.TempDir()
	diff := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	if err := AppendChangelog(dir, diff, when, ""); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	// After "## 2026-04-19\n\n" the next non-blank content should be
	// "### Added" with no stray note paragraph in between.
	want := "## 2026-04-19\n\n### Added\n"
	if !strings.Contains(got, want) {
		t.Errorf("expected %q in file, got:\n%s", want, got)
	}
}

// TestAppendChangelogSameDayReplaces — running export twice on the
// same day must end with a single section, not two stacked entries.
// The second run's diff + note wins.
func TestAppendChangelogSameDayReplaces(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	first := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	if err := AppendChangelog(dir, first, when, "first note"); err != nil {
		t.Fatal(err)
	}
	second := &ExportDiff{Updated: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo (v2)"}}}
	if err := AppendChangelog(dir, second, when, "second note"); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	if strings.Count(got, "## 2026-04-19") != 1 {
		t.Errorf("same-day entries must collapse to one section, got:\n%s", got)
	}
	if strings.Contains(got, "first note") {
		t.Errorf("first note should have been replaced, got:\n%s", got)
	}
	if !strings.Contains(got, "second note") {
		t.Errorf("second note missing:\n%s", got)
	}
	if strings.Contains(got, "### Added") {
		t.Errorf("first diff's Added group should have been replaced by second's Updated:\n%s", got)
	}
	if !strings.Contains(got, "### Updated") {
		t.Errorf("second diff's Updated group missing:\n%s", got)
	}
}

// TestAppendChangelogPrependsWhenDateDiffers — a new date always goes
// to the top, old sections stay intact below.
func TestAppendChangelogPrependsWhenDateDiffers(t *testing.T) {
	dir := t.TempDir()
	older := time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	diff := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	if err := AppendChangelog(dir, diff, older, "older"); err != nil {
		t.Fatal(err)
	}
	if err := AppendChangelog(dir, diff, newer, "newer"); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	newerIdx := strings.Index(got, "## 2026-04-19")
	olderIdx := strings.Index(got, "## 2026-04-18")
	if newerIdx < 0 || olderIdx < 0 {
		t.Fatalf("both sections should be present:\n%s", got)
	}
	if newerIdx > olderIdx {
		t.Errorf("newer section should come first:\n%s", got)
	}
	if !strings.Contains(got, "older") || !strings.Contains(got, "newer") {
		t.Errorf("both notes should survive:\n%s", got)
	}
}

// TestUpdateLatestExportNoteReplaces confirms that editing the top
// section's note preserves the group lists byte-for-byte.
func TestUpdateLatestExportNoteReplaces(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	diff := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	if err := AppendChangelog(dir, diff, when, "typo heer"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLatestExportNote(dir, "fixed typo here"); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	if strings.Contains(got, "typo heer") {
		t.Errorf("old note should have been replaced:\n%s", got)
	}
	if !strings.Contains(got, "fixed typo here") {
		t.Errorf("new note missing:\n%s", got)
	}
	if !strings.Contains(got, "### Added\n- `foo`") {
		t.Errorf("Added group must be preserved byte-for-byte:\n%s", got)
	}
}

// TestUpdateLatestExportNoteEmptyRemoves — passing empty string drops
// the note paragraph without touching the groups.
func TestUpdateLatestExportNoteEmptyRemoves(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	diff := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	if err := AppendChangelog(dir, diff, when, "note to drop"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLatestExportNote(dir, "  "); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	if strings.Contains(got, "note to drop") {
		t.Errorf("note should have been removed:\n%s", got)
	}
	// After removal the section should look like a no-note entry.
	if !strings.Contains(got, "## 2026-04-19\n\n### Added\n") {
		t.Errorf("expected clean no-note section, got:\n%s", got)
	}
}

// TestUpdateLatestExportNoteNoSections — editing fails loud when there
// is nothing to edit.
func TestUpdateLatestExportNoteNoSections(t *testing.T) {
	dir := t.TempDir()
	if err := UpdateLatestExportNote(dir, "anything"); err == nil {
		t.Errorf("expected error when CHANGELOG.md is missing")
	}
	// Now write a header-only file (no sections).
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(changelogHeader), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLatestExportNote(dir, "anything"); err == nil {
		t.Errorf("expected error when CHANGELOG.md has no sections")
	}
}

// TestCurrentExportNote — round-trips the note back through the API.
func TestCurrentExportNote(t *testing.T) {
	dir := t.TempDir()
	// Missing file → empty string, no error.
	got, err := CurrentExportNote(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("missing file should return empty, got %q", got)
	}

	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	diff := &ExportDiff{Added: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	if err := AppendChangelog(dir, diff, when, "first note\n\nsecond paragraph"); err != nil {
		t.Fatal(err)
	}
	got, err = CurrentExportNote(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "first note\n\nsecond paragraph"
	if got != want {
		t.Errorf("round-trip mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Empty note → CurrentExportNote returns "".
	if err := UpdateLatestExportNote(dir, ""); err != nil {
		t.Fatal(err)
	}
	got, err = CurrentExportNote(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("after clearing, expected empty, got %q", got)
	}
}

// TestAppendChangelogSameDayHeadingWithSuffix — hand-edited heading
// like "## 2026-04-19 (release)" still counts as the same day and
// gets replaced.
func TestAppendChangelogSameDayHeadingWithSuffix(t *testing.T) {
	dir := t.TempDir()
	initial := changelogHeader + "\n## 2026-04-19 (release)\n\nhand-edited content\n\n### Added\n- `foo` (tier1) — Foo\n\n"
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 4, 19, 0, 0, 0, 0, time.UTC)
	diff := &ExportDiff{Updated: []DiffEntry{{Slug: "foo", Category: "tier1", Name: "Foo"}}}
	if err := AppendChangelog(dir, diff, when, "replacement"); err != nil {
		t.Fatal(err)
	}
	got := readChangelog(t, dir)
	if strings.Contains(got, "(release)") {
		t.Errorf("hand-edited heading survived — should have been replaced:\n%s", got)
	}
	if strings.Contains(got, "hand-edited content") {
		t.Errorf("old content survived:\n%s", got)
	}
	if !strings.Contains(got, "replacement") {
		t.Errorf("new note missing:\n%s", got)
	}
	if strings.Count(got, "## 2026-04-19") != 1 {
		t.Errorf("must end with a single section for that date:\n%s", got)
	}
}
