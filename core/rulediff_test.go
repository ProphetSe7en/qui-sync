package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestIsRulePathQuiSync covers the layout rules for qui-sync's native
// format — rules/<cat>/<file>.json with at least one category depth.
func TestIsRulePathQuiSync(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"rules/movies/tag-tier1.json", true},
		{"rules/movies/subtype/foo.json", true},
		{"rules/tv/4k/hdr/rule.json", true},
		{"rules/movies/", false},              // no filename
		{"rules/tag-tier1.json", false},       // no category
		{"docs/readme.md", false},             // wrong top-level
		{"rules/movies/tag-tier1.yml", false}, // wrong ext
		{"not-rules/movies/foo.json", false},
		{"movies/Tag Tier 1.json", false}, // TRaSH path rejected
	}
	for _, tc := range cases {
		if got := LayoutQuiSync.IsRulePath(tc.path); got != tc.want {
			t.Errorf("LayoutQuiSync.IsRulePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestIsRulePathTRaSH covers the TRaSH layout — category dir at repo
// root (movies / series) with rule JSON files directly beneath.
func TestIsRulePathTRaSH(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"movies/Tag Tier 1.json", true},
		{"series/Resume stopped cross-seeds.json", true},
		{"movies/nested/foo.json", false},    // no nested under TRaSH
		{"rules/movies/tag-tier1.json", false}, // qui-sync path rejected
		{"docs/readme.md", false},
		{"other/Tag.json", false}, // unknown category
	}
	for _, tc := range cases {
		if got := LayoutTRaSH.IsRulePath(tc.path); got != tc.want {
			t.Errorf("LayoutTRaSH.IsRulePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestCategoryFromPath confirms nested categories join with "/" under
// qui-sync, and the root directory alone is the category under TRaSH.
func TestCategoryFromPath(t *testing.T) {
	cases := []struct {
		layout RepoLayout
		path   string
		want   string
	}{
		{LayoutQuiSync, "rules/movies/tag-tier1.json", "movies"},
		{LayoutQuiSync, "rules/movies/subtype/foo.json", "movies/subtype"},
		{LayoutQuiSync, "rules/tv/4k/hdr/rule.json", "tv/4k/hdr"},
		{LayoutTRaSH, "movies/Tag Tier 1.json", "movies"},
		{LayoutTRaSH, "series/Resume.json", "series"},
	}
	for _, tc := range cases {
		if got := tc.layout.CategoryFromPath(tc.path); got != tc.want {
			t.Errorf("%s.CategoryFromPath(%q) = %q, want %q", tc.layout, tc.path, got, tc.want)
		}
	}
}

// TestRulePath confirms that writing a rule in each layout produces the
// expected path on disk.
func TestRulePath(t *testing.T) {
	cases := []struct {
		layout             RepoLayout
		category, slug, name string
		want               string
	}{
		{LayoutQuiSync, "movies", "tag-tier1", "Tag: Tier 1", "rules/movies/tag-tier1.json"},
		{LayoutTRaSH, "movies", "tag-tier1", "Tag: Tier 1", "movies/Tag Tier 1.json"},
		{LayoutUnknown, "movies", "tag-tier1", "Tag: Tier 1", "rules/movies/tag-tier1.json"},
	}
	for _, tc := range cases {
		if got := tc.layout.RulePath(tc.category, tc.slug, tc.name); got != tc.want {
			t.Errorf("%s.RulePath(%q,%q,%q) = %q, want %q",
				tc.layout, tc.category, tc.slug, tc.name, got, tc.want)
		}
	}
}

// TestDetectLayout builds tiny fixture repos in a temp dir and confirms
// DetectLayout returns the expected enum for each.
func TestDetectLayout(t *testing.T) {
	t.Run("qui-sync layout wins when rules/ has content", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir+"/rules/movies/tag-tier1.json", `{"name":"Tag: Tier 1"}`)
		if got := DetectLayout(dir); got != LayoutQuiSync {
			t.Errorf("got %q, want %q", got, LayoutQuiSync)
		}
	})
	t.Run("TRaSH layout on movies/ at root", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir+"/movies/Tag Tier 1.json", `{"name":"Tag: Tier 1"}`)
		if got := DetectLayout(dir); got != LayoutTRaSH {
			t.Errorf("got %q, want %q", got, LayoutTRaSH)
		}
	})
	t.Run("TRaSH layout on series/ at root", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir+"/series/Tag Tier 1.json", `{"name":"Tag: Tier 1"}`)
		if got := DetectLayout(dir); got != LayoutTRaSH {
			t.Errorf("got %q, want %q", got, LayoutTRaSH)
		}
	})
	t.Run("Unknown for empty repo", func(t *testing.T) {
		dir := t.TempDir()
		if got := DetectLayout(dir); got != LayoutUnknown {
			t.Errorf("got %q, want %q", got, LayoutUnknown)
		}
	})
	t.Run("Unknown when rules/ and movies/ both empty", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(dir+"/rules/movies", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir+"/movies", 0o755); err != nil {
			t.Fatal(err)
		}
		if got := DetectLayout(dir); got != LayoutUnknown {
			t.Errorf("got %q, want %q", got, LayoutUnknown)
		}
	})
	t.Run("qui-sync wins over TRaSH when both somehow coexist", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, dir+"/rules/movies/tag-tier1.json", `{"name":"Tag: Tier 1"}`)
		mustWrite(t, dir+"/movies/Tag Tier 1.json", `{"name":"Tag: Tier 1"}`)
		if got := DetectLayout(dir); got != LayoutQuiSync {
			t.Errorf("got %q, want %q (qui-sync prefix should win)", got, LayoutQuiSync)
		}
	})
}

// mustWrite is a tiny test helper that creates parent dirs and writes
// content to path, failing the test on any error.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTRaSHFilename covers the filesystem-safety rules we apply when
// converting a rule name into a TRaSH-style filename.
func TestTRaSHFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Tag: Tier 1", "Tag Tier 1"},
		{"Resume stopped cross-seeds (greater 90%)", "Resume stopped cross-seeds (greater 90%)"},
		{"foo/bar:baz", "foobarbaz"},
		{"  leading  ", "leading"},
		{"double  space", "double space"},
	}
	for _, tc := range cases {
		if got := TRaSHFilename(tc.in); got != tc.want {
			t.Errorf("TRaSHFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestScalarArrayDiffSortedAndDeterministic is the regression anchor for
// the sorting fix — map iteration in Go is random, so without a sort the
// resulting diff would flicker between refreshes. Run the same diff many
// times and confirm the output is byte-identical each time.
func TestScalarArrayDiffSortedAndDeterministic(t *testing.T) {
	before := []any{"beta", "alpha", "gamma"}
	after := []any{"beta", "delta", "epsilon"}

	// Run repeatedly — if sorting were absent, map iteration would produce
	// different orderings over enough runs.
	var firstAdded, firstRemoved []any
	for i := 0; i < 50; i++ {
		added, removed := scalarArrayDiff(before, after)
		if i == 0 {
			firstAdded = added
			firstRemoved = removed
			continue
		}
		if !reflect.DeepEqual(added, firstAdded) {
			t.Fatalf("added order flips between runs: run %d got %v, first %v", i, added, firstAdded)
		}
		if !reflect.DeepEqual(removed, firstRemoved) {
			t.Fatalf("removed order flips between runs: run %d got %v, first %v", i, removed, firstRemoved)
		}
	}

	// And the sort result is actually sorted.
	if !reflect.DeepEqual(firstAdded, []any{"delta", "epsilon"}) {
		t.Errorf("added not sorted: %v", firstAdded)
	}
	if !reflect.DeepEqual(firstRemoved, []any{"alpha", "gamma"}) {
		t.Errorf("removed not sorted: %v", firstRemoved)
	}
}

// TestStructuralDiffScalarChanges covers the three basic cases: key only
// on one side (renders as "scalar" so the UI can show "(none) → value"),
// changed scalar, and unchanged scalar (must be absent from output).
func TestStructuralDiffScalarChanges(t *testing.T) {
	before := map[string]any{
		"name":     "Old",
		"enabled":  true,
		"obsolete": "gone",
	}
	after := map[string]any{
		"name":     "New",
		"enabled":  true,
		"added":    "fresh",
	}
	out := structuralDiff(before, after, "")

	// Expected: name changed, obsolete removed, added added. enabled omitted.
	if len(out) != 3 {
		t.Fatalf("expected 3 diffs, got %d: %+v", len(out), out)
	}

	byPath := map[string]FieldDiff{}
	for _, d := range out {
		byPath[d.Path] = d
	}

	if d, ok := byPath["added"]; !ok {
		t.Error("missing diff for added key")
	} else if d.Kind != "scalar" || d.Before != nil || d.After != "fresh" {
		t.Errorf("added key diff unexpected: %+v", d)
	}

	if d, ok := byPath["obsolete"]; !ok {
		t.Error("missing diff for obsolete key")
	} else if d.Kind != "scalar" || d.Before != "gone" || d.After != nil {
		t.Errorf("obsolete key diff unexpected: %+v", d)
	}

	if d, ok := byPath["name"]; !ok {
		t.Error("missing diff for name key")
	} else if d.Kind != "scalar" || d.Before != "Old" || d.After != "New" {
		t.Errorf("name key diff unexpected: %+v", d)
	}

	if _, ok := byPath["enabled"]; ok {
		t.Errorf("unchanged key should not appear in diff")
	}
}

// TestStructuralDiffArraySemantics confirms arrays of scalars collapse
// into array_added / array_removed so the UI reports semantic change
// instead of whole-array replacement.
func TestStructuralDiffArraySemantics(t *testing.T) {
	before := map[string]any{"tags": []any{"alpha", "beta"}}
	after := map[string]any{"tags": []any{"beta", "gamma"}}
	out := structuralDiff(before, after, "")

	if len(out) != 2 {
		t.Fatalf("expected 2 diffs (one added + one removed), got %d: %+v", len(out), out)
	}
	var added, removed *FieldDiff
	for i := range out {
		if out[i].Kind == "array_added" {
			added = &out[i]
		}
		if out[i].Kind == "array_removed" {
			removed = &out[i]
		}
	}
	if added == nil || removed == nil {
		t.Fatalf("missing array_added or array_removed: %+v", out)
	}
	if !reflect.DeepEqual(added.After, []any{"gamma"}) {
		t.Errorf("array_added.After = %+v", added.After)
	}
	if !reflect.DeepEqual(removed.Before, []any{"alpha"}) {
		t.Errorf("array_removed.Before = %+v", removed.Before)
	}
}

// TestStructuralDiffNestedObjects confirms recursion into nested objects
// and the dotted path it produces.
func TestStructuralDiffNestedObjects(t *testing.T) {
	before := map[string]any{
		"conditions": map[string]any{
			"ratio":  float64(2.0),
			"status": "active",
		},
	}
	after := map[string]any{
		"conditions": map[string]any{
			"ratio":  float64(3.0),
			"status": "active",
		},
	}
	out := structuralDiff(before, after, "")

	if len(out) != 1 {
		t.Fatalf("expected 1 diff (nested ratio change), got %d: %+v", len(out), out)
	}
	if out[0].Path != "conditions.ratio" {
		t.Errorf("expected path conditions.ratio, got %s", out[0].Path)
	}
	if out[0].Kind != "scalar" {
		t.Errorf("expected scalar kind, got %s", out[0].Kind)
	}
}

// TestExtractRuleName exercises the fallback path — a malformed or
// name-less rule file still yields a viewable row (slug used).
func TestExtractRuleName(t *testing.T) {
	named := json.RawMessage(`{"name":"Tier 1","other":42}`)
	if got := extractRuleName(named, "tier-1"); got != "Tier 1" {
		t.Errorf("named: got %q, want Tier 1", got)
	}
	noName := json.RawMessage(`{"other":42}`)
	if got := extractRuleName(noName, "tier-1"); got != "tier-1" {
		t.Errorf("no name: got %q, want tier-1 (slug fallback)", got)
	}
	malformed := json.RawMessage(`not json at all`)
	if got := extractRuleName(malformed, "tier-1"); got != "tier-1" {
		t.Errorf("malformed: got %q, want tier-1 (slug fallback)", got)
	}
}
