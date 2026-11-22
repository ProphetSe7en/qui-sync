package core

import (
	"strings"
	"testing"
)

func TestBuildExportCommitMessageEmpty(t *testing.T) {
	got := BuildExportCommitMessage(&ExportDiff{})
	if got != "qui-sync export" {
		t.Fatalf("empty diff: got %q", got)
	}
	if got := BuildExportCommitMessage(nil); got != "qui-sync export" {
		t.Fatalf("nil diff: got %q", got)
	}
}

func TestBuildExportCommitMessageSingleAdd(t *testing.T) {
	diff := &ExportDiff{
		Added: []DiffEntry{{Slug: "foo", Category: "movies", Name: "Foo Tier 1"}},
	}
	got := BuildExportCommitMessage(diff)
	wantSubject := "qui-sync: add movies/Foo Tier 1"
	if !strings.HasPrefix(got, wantSubject+"\n") {
		t.Fatalf("subject mismatch: got %q", got)
	}
	if !strings.Contains(got, "Added:\n- movies/Foo Tier 1") {
		t.Fatalf("body missing Added section: %q", got)
	}
}

func TestBuildExportCommitMessageSingleRename(t *testing.T) {
	diff := &ExportDiff{
		Renamed: []DiffEntry{{Slug: "foo", Category: "movies", OldName: "Foo", Name: "Foo (HD)"}},
	}
	got := BuildExportCommitMessage(diff)
	want := "qui-sync: rename movies/Foo → Foo (HD)"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("rename subject: got %q want prefix %q", got, want)
	}
}

func TestBuildExportCommitMessageSameKindMany(t *testing.T) {
	diff := &ExportDiff{
		Renamed: []DiffEntry{
			{Slug: "a", Category: "movies", OldName: "A", Name: "A (HD)"},
			{Slug: "b", Category: "movies", OldName: "B", Name: "B (HD)"},
		},
	}
	got := BuildExportCommitMessage(diff)
	if !strings.HasPrefix(got, "qui-sync export — renamed 2 rules\n") {
		t.Fatalf("multi-rename subject: got %q", got)
	}
}

func TestBuildExportCommitMessageMixed(t *testing.T) {
	diff := &ExportDiff{
		Added:   []DiffEntry{{Slug: "a", Category: "movies", Name: "Alpha"}},
		Updated: []DiffEntry{{Slug: "b", Category: "movies", Name: "Bravo"}},
	}
	got := BuildExportCommitMessage(diff)
	if !strings.HasPrefix(got, "qui-sync export — 1 updated, 1 added\n") {
		t.Fatalf("mixed subject: got %q", got)
	}
	if !strings.Contains(got, "Added:\n- movies/Alpha") {
		t.Fatalf("body missing Added: %q", got)
	}
	if !strings.Contains(got, "Updated:\n- movies/Bravo") {
		t.Fatalf("body missing Updated: %q", got)
	}
}

// A large mixed export is the case GitHub's file list shows worst: the
// subject is the only text there, so it has to carry the shape of the
// change rather than a bare total.
func TestBuildExportCommitMessageLargeMixedNamesTheMix(t *testing.T) {
	diff := &ExportDiff{}
	for i := 0; i < 41; i++ {
		diff.Updated = append(diff.Updated, DiffEntry{Slug: "u", Category: "series", Name: "Updated rule"})
	}
	diff.Added = append(diff.Added, DiffEntry{Slug: "a", Category: "series", Name: "New rule"})
	diff.Removed = append(diff.Removed, DiffEntry{Slug: "r", Category: "series", Name: "Gone rule"})

	got := BuildExportCommitMessage(diff)
	if !strings.HasPrefix(got, "qui-sync export — 41 updated, 1 added, 1 archived\n") {
		t.Fatalf("large mixed subject: got %q", strings.SplitN(got, "\n", 2)[0])
	}
}

func TestBuildExportCommitMessageComment(t *testing.T) {
	diff := &ExportDiff{
		Renamed: []DiffEntry{{
			Slug: "foo", Category: "movies", OldName: "Foo", Name: "Foo (HD)",
			Comment: "added HD suffix so Sonarr 4K-profile does not match",
		}},
	}
	got := BuildExportCommitMessage(diff)
	if !strings.Contains(got, "- movies/Foo → Foo (HD)\n  added HD suffix so Sonarr 4K-profile does not match") {
		t.Fatalf("comment not rendered as continuation: %q", got)
	}
}

func TestBuildExportCommitMessageMultilineComment(t *testing.T) {
	diff := &ExportDiff{
		Added: []DiffEntry{{
			Slug: "f", Category: "movies", Name: "Foo",
			Comment: "first line\nsecond line",
		}},
	}
	got := BuildExportCommitMessage(diff)
	if !strings.Contains(got, "- movies/Foo\n  first line\n  second line") {
		t.Fatalf("multiline comment indentation: %q", got)
	}
}

func TestRuleKeyRoundTrip(t *testing.T) {
	e := DiffEntry{InstanceID: 7, QuiID: 42}
	if got, want := e.RuleKey(), "7:42"; got != want {
		t.Fatalf("RuleKey: got %q want %q", got, want)
	}
}

func TestExportOptionsIsIncluded(t *testing.T) {
	t.Run("nil_include_means_all", func(t *testing.T) {
		o := ExportOptions{}
		if !o.isIncluded(1, 2) {
			t.Fatal("nil Include should include everything")
		}
	})
	t.Run("explicit_set", func(t *testing.T) {
		o := ExportOptions{Include: map[string]bool{"1:2": true}}
		if !o.isIncluded(1, 2) {
			t.Fatal("present key should include")
		}
		if o.isIncluded(1, 3) {
			t.Fatal("absent key should NOT include")
		}
	})
	t.Run("empty_map_excludes_everything", func(t *testing.T) {
		o := ExportOptions{Include: map[string]bool{}}
		if o.isIncluded(1, 2) {
			t.Fatal("empty Include map should exclude everything")
		}
	})
}

// The note is what the maintainer wrote about why the export happened,
// so it carries more than a generated count in a commit list. The
// generated summary still has to survive, in the body.
func TestBuildExportCommitMessageUsesNoteAsSubject(t *testing.T) {
	diff := &ExportDiff{Updated: []DiffEntry{
		{Slug: "delete-nohl-tier1", Category: "series", Name: "Delete: noHL Tier 1"},
		{Slug: "delete-nohl-tier2", Category: "series", Name: "Delete: noHL Tier 2"},
	}}
	note := "Changed the condition from Completion Time to Seeding Time"

	got := BuildExportCommitMessageWithNote(diff, note)
	subject, body, _ := strings.Cut(got, "\n")
	if subject != note {
		t.Errorf("subject = %q, want the note", subject)
	}
	if !strings.Contains(body, "qui-sync export — updated 2 rules") {
		t.Errorf("body should keep the generated summary:\n%s", body)
	}
	if !strings.Contains(body, "Updated:\n- series/Delete: noHL Tier 1") {
		t.Errorf("body should still list the rules:\n%s", body)
	}
}

func TestBuildExportCommitMessageWithoutNoteIsUnchanged(t *testing.T) {
	diff := &ExportDiff{Updated: []DiffEntry{{Slug: "a", Category: "movies", Name: "Alpha"}}}
	if BuildExportCommitMessageWithNote(diff, "  ") != BuildExportCommitMessage(diff) {
		t.Error("a blank note must not change the message")
	}
}

// A long or multi-line note still has to produce a readable one-line
// subject, without losing the rest of what was written.
func TestBuildExportCommitMessageShortensLongNote(t *testing.T) {
	diff := &ExportDiff{Updated: []DiffEntry{{Slug: "a", Category: "movies", Name: "Alpha"}}}
	note := "Changed the condition from Completion Time to Seeding Time to prevent torrents from stopping before the preferred 21-day seeding period is reached."

	got := BuildExportCommitMessageWithNote(diff, note)
	subject, body, _ := strings.Cut(got, "\n")
	if len([]rune(subject)) > commitSubjectMaxLen {
		t.Errorf("subject is %d runes, want at most %d: %q", len([]rune(subject)), commitSubjectMaxLen, subject)
	}
	if !strings.HasSuffix(subject, "…") {
		t.Errorf("a shortened subject should say so, got %q", subject)
	}
	if !strings.Contains(got, "21-day seeding period is reached.") {
		t.Errorf("the rest of the note must survive in the body:\n%s", body)
	}
}

func TestBuildExportCommitMessageMultilineNote(t *testing.T) {
	diff := &ExportDiff{Updated: []DiffEntry{{Slug: "a", Category: "movies", Name: "Alpha"}}}
	note := "Switched to seeding time\n\nTorrents were stopping too early on the 21-day trackers."

	got := BuildExportCommitMessageWithNote(diff, note)
	subject, _, _ := strings.Cut(got, "\n")
	if subject != "Switched to seeding time" {
		t.Errorf("subject = %q, want the note's first line", subject)
	}
	if !strings.Contains(got, "Torrents were stopping too early") {
		t.Errorf("the remaining lines must survive:\n%s", got)
	}
}
