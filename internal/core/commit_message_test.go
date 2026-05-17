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
	if !strings.HasPrefix(got, "qui-sync export — 2 changes\n") {
		t.Fatalf("mixed subject: got %q", got)
	}
	if !strings.Contains(got, "Added:\n- movies/Alpha") {
		t.Fatalf("body missing Added: %q", got)
	}
	if !strings.Contains(got, "Updated:\n- movies/Bravo") {
		t.Fatalf("body missing Updated: %q", got)
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
