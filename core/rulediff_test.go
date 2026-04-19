package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestIsRulePath covers the layout rules that decide which files participate
// in the diff. Accepts "rules/<cat>/<file>.json" and nested categories,
// rejects everything else.
func TestIsRulePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"rules/movies/tag-tier1.json", true},
		{"rules/movies/subtype/foo.json", true},
		{"rules/tv/4k/hdr/rule.json", true},
		{"rules/movies/", false},           // no filename
		{"rules/tag-tier1.json", false},    // no category
		{"docs/readme.md", false},          // wrong top-level
		{"rules/movies/tag-tier1.yml", false}, // wrong ext
		{"not-rules/movies/foo.json", false},
	}
	for _, tc := range cases {
		if got := isRulePath(tc.path); got != tc.want {
			t.Errorf("isRulePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestCategoryFromPath confirms nested categories join with "/" so the UI
// can render them as-is without further processing.
func TestCategoryFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"rules/movies/tag-tier1.json", "movies"},
		{"rules/movies/subtype/foo.json", "movies/subtype"},
		{"rules/tv/4k/hdr/rule.json", "tv/4k/hdr"},
	}
	for _, tc := range cases {
		if got := categoryFromPath(tc.path); got != tc.want {
			t.Errorf("categoryFromPath(%q) = %q, want %q", tc.path, got, tc.want)
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
