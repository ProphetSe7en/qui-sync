package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestIsRulePath covers the share-repo convention — "<category>/<file>.json"
// with exactly two path segments, both non-empty, first segment not a
// hidden directory.
func TestIsRulePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"movies/Tag Tier 1.json", true},
		{"series/Resume stopped cross-seeds.json", true},
		{"misc/Custom Rule.json", true},
		{"movies/", false},                                 // no filename
		{"Tag Tier 1.json", false},                         // no category
		{"movies/nested/foo.json", false},                  // too deep
		{"movies/Tag Tier 1.yml", false},                   // wrong ext
		{".github/workflows/build.yml", false},             // dot dir
		{".git/config", false},                             // dot dir
	}
	for _, tc := range cases {
		if got := isRulePath(tc.path); got != tc.want {
			t.Errorf("isRulePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestCategoryFromPath confirms the first segment wins; all rule paths
// are "<cat>/<file>.json" so the category is simply the first component.
func TestCategoryFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"movies/Tag Tier 1.json", "movies"},
		{"series/Resume.json", "series"},
		{"misc/Custom.json", "misc"},
	}
	for _, tc := range cases {
		if got := categoryFromPath(tc.path); got != tc.want {
			t.Errorf("categoryFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestRuleFilename covers the filesystem-safety rules we apply when
// converting a rule name into its on-disk filename. Filesystem-unsafe
// characters are stripped; whitespace is collapsed; case is preserved.
func TestRuleFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Tag: Tier 1", "Tag Tier 1"},
		{"Resume stopped cross-seeds (greater 90%)", "Resume stopped cross-seeds (greater 90%)"},
		{"foo/bar:baz", "foobarbaz"},
		{"  leading  ", "leading"},
		{"double  space", "double space"},
	}
	for _, tc := range cases {
		if got := RuleFilename(tc.in); got != tc.want {
			t.Errorf("RuleFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestIsRuleJSON separates real rule files from anything else a
// maintainer might keep in the repo (README, LICENSE, CI yaml, stray
// JSON with no "name" field).
func TestIsRuleJSON(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"valid rule", `{"name": "Tag Tier 1", "conditions": {}}`, true},
		{"missing name", `{"conditions": {}}`, false},
		{"empty name", `{"name": ""}`, false},
		{"malformed JSON", `{not json`, false},
		{"non-object JSON", `["array"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRuleJSON([]byte(tc.data)); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
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

// TestWalkLocalRulesFilters confirms the walker picks up rule files,
// ignores non-rule JSON, and skips hidden directories and archive/.
func TestWalkLocalRulesFilters(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/movies/Tag Tier 1.json", `{"name":"Tag: Tier 1"}`)
	mustWrite(t, dir+"/series/Tag Tier 2.json", `{"name":"Tag: Tier 2"}`)
	mustWrite(t, dir+"/scripts/helper.json", `{"just": "data"}`) // not a rule
	mustWrite(t, dir+"/docs/readme.md", `# docs`)                // not json
	mustWrite(t, dir+"/.github/workflows/ci.yml", `name: ci`)    // hidden dir
	mustWrite(t, dir+"/archive/movies/Old Rule.json", `{"name":"Old Rule"}`) // retired, should be skipped

	rules, err := walkLocalRules(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d: %+v", len(rules), rules)
	}
	seen := map[string]bool{}
	for _, r := range rules {
		seen[r.relPath] = true
	}
	if !seen["movies/Tag Tier 1.json"] {
		t.Error("missing movies/Tag Tier 1.json")
	}
	if !seen["series/Tag Tier 2.json"] {
		t.Error("missing series/Tag Tier 2.json")
	}
	for path := range seen {
		if strings.HasPrefix(path, "archive/") {
			t.Errorf("archive files must not appear in the walker: %s", path)
		}
	}
}

// TestIsRulePathRejectsArchive confirms that paths under archive/ are
// not classified as active rule files — so the rule-diff never reports
// retired rules as present in the working tree or remote.
func TestIsRulePathRejectsArchive(t *testing.T) {
	cases := map[string]bool{
		"movies/Tag Tier 1.json":         true,
		"archive/movies/Old.json":        false, // retired
		"archive/series/Something.json":  false,
	}
	for path, want := range cases {
		if got := isRulePath(path); got != want {
			t.Errorf("isRulePath(%q) = %v, want %v", path, got, want)
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
