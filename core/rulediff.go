package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// ChangeKind categorises how a rule differs between the working tree and
// the configured remote.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"    // local has it, remote does not
	ChangeModified ChangeKind = "modified" // both have it, content differs
	ChangeRemoved  ChangeKind = "removed"  // remote has it, local does not
)

// FieldDiff is a single structural difference between the local and remote
// versions of a rule. Arrays of scalars are collapsed into "added" / "removed"
// items so the UI shows semantic changes rather than raw list replacement.
type FieldDiff struct {
	// Path is the dotted JSON path into the rule object — "name",
	// "conditions.tags", "actions[2].type", etc.
	Path string `json:"path"`

	// Kind describes how to render this diff in the UI.
	//   "scalar"        — primitive before → after
	//   "array_added"   — array items present in After but not Before
	//   "array_removed" — array items present in Before but not After
	//   "object"        — complex before/after, show pretty-printed JSON blocks
	Kind string `json:"kind"`

	// Before / After carry the raw JSON values. For "array_added" only
	// After is set; for "array_removed" only Before.
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
}

// RuleDiff represents one rule whose state differs between local and
// remote — or one rule that is new / deleted entirely.
type RuleDiff struct {
	// Path is the rule's file path relative to the repo root,
	// e.g. "rules/movies/tag-tier1.json".
	Path string `json:"path"`

	// Category is the folder component under rules/ — "movies", "tv", etc.
	Category string `json:"category"`

	// Slug is the filename without extension — stable ID for the rule.
	Slug string `json:"slug"`

	// Name is the human-readable rule name, pulled from the "name" field
	// of whichever side of the diff exists. Falls back to Slug if missing.
	Name string `json:"name"`

	// Change is one of "added", "modified", "removed".
	Change ChangeKind `json:"change"`

	// Fields is populated only for "modified" entries — the per-field
	// differences. Shallow for arrays of scalars; recursive for objects.
	Fields []FieldDiff `json:"fields,omitempty"`

	// BeforeJSON / AfterJSON carry the full rule on the side that exists
	// so the UI can render "here is the rule you are about to push /
	// delete / bring in from remote" when the row is expanded.
	BeforeJSON json.RawMessage `json:"before_json,omitempty"`
	AfterJSON  json.RawMessage `json:"after_json,omitempty"`
}

// RuleDiffResult groups rule-level diffs for the repo, organised by change
// type so the UI can render three sections.
type RuleDiffResult struct {
	Added    []RuleDiff `json:"added"`
	Modified []RuleDiff `json:"modified"`
	Removed  []RuleDiff `json:"removed"`

	// NoRemote is true when the repo has no remote branch to compare
	// against (no origin, branch does not exist, never pushed). In that
	// state every local rule appears as "added" so the user can still
	// preview what the first push will look like.
	NoRemote bool `json:"no_remote"`

	// NoRepo is true when /data/repo does not contain a git working tree
	// yet — nothing has been exported. The UI renders an explanatory
	// empty state instead of three empty lists.
	NoRepo bool `json:"no_repo"`
}

// ComputeRepoRuleDiff produces the full rule-level diff for the repo at
// repoDir. It compares the working tree (including uncommitted changes)
// against origin/<current-branch>. Files that are not under rules/ are
// ignored — the share-repo layout is rules/<category>/<slug>.json.
func ComputeRepoRuleDiff(ctx context.Context, repoDir string) (*RuleDiffResult, error) {
	result := &RuleDiffResult{
		Added:    []RuleDiff{},
		Modified: []RuleDiff{},
		Removed:  []RuleDiff{},
	}

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		result.NoRepo = true
		return result, nil
	}

	branch, err := runGit(ctx, repoDir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("determine branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	remoteRef := "origin/" + branch

	// Does origin/<branch> exist locally? If not, treat every local rule
	// as added and skip remote enumeration.
	if _, err := runGit(ctx, repoDir, nil, "rev-parse", "--verify", remoteRef); err != nil {
		result.NoRemote = true
		locals, err := walkLocalRules(repoDir)
		if err != nil {
			return nil, err
		}
		for _, f := range locals {
			result.Added = append(result.Added, newAddedRuleDiff(f))
		}
		sortResult(result)
		return result, nil
	}

	locals, err := walkLocalRules(repoDir)
	if err != nil {
		return nil, err
	}

	// Index of remote file paths (relative to repo root). Uses -z so paths
	// containing spaces, quotes, or other special chars come through as
	// literal NUL-separated strings instead of git's default C-style
	// quoting — otherwise "foo bar.json" would be quoted on the remote
	// side and never match the literal path produced by filepath.WalkDir.
	remoteOut, err := runGit(ctx, repoDir, nil, "ls-tree", "-r", "-z", "--name-only", remoteRef)
	if err != nil {
		return nil, fmt.Errorf("list remote tree: %w", err)
	}
	remoteSet := map[string]bool{}
	for _, p := range strings.Split(remoteOut, "\x00") {
		if p != "" && isRulePath(p) {
			remoteSet[p] = true
		}
	}

	// Walk local rules, classify each as added or modified.
	localSet := map[string]bool{}
	for _, lf := range locals {
		localSet[lf.relPath] = true
		if !remoteSet[lf.relPath] {
			result.Added = append(result.Added, newAddedRuleDiff(lf))
			continue
		}
		// Both sides have the file — compare content.
		remoteBytes, err := gitShowFile(ctx, repoDir, remoteRef, lf.relPath)
		if err != nil {
			return nil, fmt.Errorf("read remote %s: %w", lf.relPath, err)
		}
		rd, err := compareRuleBytes(lf, remoteBytes)
		if err != nil {
			return nil, err
		}
		if rd != nil {
			result.Modified = append(result.Modified, *rd)
		}
	}

	// Walk remote-only files → removed.
	for remotePath := range remoteSet {
		if localSet[remotePath] {
			continue
		}
		remoteBytes, err := gitShowFile(ctx, repoDir, remoteRef, remotePath)
		if err != nil {
			return nil, fmt.Errorf("read remote %s: %w", remotePath, err)
		}
		result.Removed = append(result.Removed, newRemovedRuleDiff(remotePath, remoteBytes))
	}

	sortResult(result)
	return result, nil
}

// localRuleFile is an internal helper tying together the on-disk path
// information with its JSON content so we only read each file once.
type localRuleFile struct {
	relPath  string // "rules/movies/tag-tier1.json"
	category string // "movies"
	slug     string // "tag-tier1"
	content  []byte
}

func walkLocalRules(repoDir string) ([]localRuleFile, error) {
	var out []localRuleFile
	rulesRoot := filepath.Join(repoDir, "rules")
	if _, err := os.Stat(rulesRoot); err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	err := filepath.WalkDir(rulesRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".json" {
			return nil
		}
		rel, err := filepath.Rel(repoDir, p)
		if err != nil {
			return err
		}
		// filepath.WalkDir yields OS-native separators. Normalise to "/"
		// for consistent comparison with the forward-slash paths git uses.
		rel = filepath.ToSlash(rel)
		if !isRulePath(rel) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, localRuleFile{
			relPath:  rel,
			category: categoryFromPath(rel),
			slug:     slugFromPath(rel),
			content:  b,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// isRulePath returns true when p is a valid rule file in the share-repo
// layout — "rules/.../<slug>.json" with at least one subdirectory depth.
// Nested categories like "rules/movies/4k/foo.json" are accepted; the full
// path between "rules/" and the filename becomes the category string.
func isRulePath(p string) bool {
	if !strings.HasPrefix(p, "rules/") {
		return false
	}
	if filepath.Ext(p) != ".json" {
		return false
	}
	// Require at least rules/<something>/<file>.json — three parts.
	parts := strings.Split(p, "/")
	return len(parts) >= 3 && parts[len(parts)-1] != ""
}

// categoryFromPath returns the directory segment(s) between "rules/" and
// the filename. For "rules/movies/tag-tier1.json" it returns "movies";
// for "rules/movies/4k/foo.json" it returns "movies/4k".
func categoryFromPath(relPath string) string {
	parts := strings.Split(relPath, "/")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[1:len(parts)-1], "/")
}

func slugFromPath(relPath string) string {
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// gitShowFile returns the content of a file at a specific git ref using
// the "<ref>:<path>" form of git show. This form is already unambiguous —
// git parses it specifically as "the blob at <path> in <ref>" — so a
// pathspec "--" separator is not required.
func gitShowFile(ctx context.Context, repoDir, ref, relPath string) ([]byte, error) {
	out, err := runGit(ctx, repoDir, nil, "show", ref+":"+relPath)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// newAddedRuleDiff builds a RuleDiff for a rule that exists locally but
// not on the remote. AfterJSON carries the full local content so the UI
// can render the full rule on expand.
func newAddedRuleDiff(f localRuleFile) RuleDiff {
	name := extractRuleName(f.content, f.slug)
	return RuleDiff{
		Path:      f.relPath,
		Category:  f.category,
		Slug:      f.slug,
		Name:      name,
		Change:    ChangeAdded,
		AfterJSON: json.RawMessage(f.content),
	}
}

// newRemovedRuleDiff builds a RuleDiff for a rule that exists on the
// remote but not locally. BeforeJSON carries the full remote content so
// the user can see what will disappear from the remote after push.
func newRemovedRuleDiff(relPath string, remoteBytes []byte) RuleDiff {
	slug := slugFromPath(relPath)
	name := extractRuleName(remoteBytes, slug)
	return RuleDiff{
		Path:       relPath,
		Category:   categoryFromPath(relPath),
		Slug:       slug,
		Name:       name,
		Change:     ChangeRemoved,
		BeforeJSON: json.RawMessage(remoteBytes),
	}
}

// compareRuleBytes parses both sides as JSON objects and returns a
// RuleDiff if they differ. Returns nil if the content is byte-identical
// or semantically identical.
func compareRuleBytes(local localRuleFile, remoteBytes []byte) (*RuleDiff, error) {
	var before, after map[string]any
	if err := json.Unmarshal(remoteBytes, &before); err != nil {
		return nil, fmt.Errorf("parse remote %s: %w", local.relPath, err)
	}
	if err := json.Unmarshal(local.content, &after); err != nil {
		return nil, fmt.Errorf("parse local %s: %w", local.relPath, err)
	}
	if reflect.DeepEqual(before, after) {
		return nil, nil
	}
	fields := structuralDiff(before, after, "")
	name := extractRuleName(local.content, local.slug)
	return &RuleDiff{
		Path:       local.relPath,
		Category:   local.category,
		Slug:       local.slug,
		Name:       name,
		Change:     ChangeModified,
		Fields:     fields,
		BeforeJSON: json.RawMessage(remoteBytes),
		AfterJSON:  json.RawMessage(local.content),
	}, nil
}

// extractRuleName pulls the "name" field from a rule's JSON, falling back
// to the slug if absent. Errors fall back silently to the slug so a
// malformed file still produces a viewable row.
func extractRuleName(content []byte, slug string) string {
	var probe struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(content, &probe); err == nil && probe.Name != "" {
		return probe.Name
	}
	return slug
}

// structuralDiff walks two JSON maps and produces a flat list of field
// differences. Arrays of scalars are collapsed into array_added /
// array_removed entries so semantic change is what surfaces. Nested
// objects recurse. Heterogeneous cases (array ↔ scalar, etc.) fall back
// to an "object" entry carrying the full before/after.
//
// When a key exists only on one side, the emitted FieldDiff carries the
// value and uses kind "scalar" so the UI can render "(none) → <value>"
// or "<value> → (none)" — what the user needs to see. Using "object" or
// "array" kind for this case would make the UI render "(object changed)"
// which is wrong: the object wasn't changed, it was added or removed.
func structuralDiff(before, after map[string]any, prefix string) []FieldDiff {
	var out []FieldDiff
	keys := unionKeys(before, after)
	sort.Strings(keys)
	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		bv, bOK := before[k]
		av, aOK := after[k]

		switch {
		case !bOK:
			out = append(out, FieldDiff{Path: path, Kind: "scalar", After: av})
		case !aOK:
			out = append(out, FieldDiff{Path: path, Kind: "scalar", Before: bv})
		case reflect.DeepEqual(bv, av):
			// unchanged — skip
		default:
			out = append(out, diffValue(path, bv, av)...)
		}
	}
	return out
}

// diffValue compares two values at the same path and produces one or more
// FieldDiff entries. For arrays of scalars it emits array_added/removed;
// for nested objects it recurses; for anything else it emits one entry
// with the raw before/after.
func diffValue(path string, before, after any) []FieldDiff {
	// Both objects → recurse
	if bo, ok := before.(map[string]any); ok {
		if ao, ok := after.(map[string]any); ok {
			return structuralDiff(bo, ao, path)
		}
	}
	// Both arrays: consider scalar-array special-case
	if ba, ok := before.([]any); ok {
		if aa, ok := after.([]any); ok {
			if isScalarArray(ba) && isScalarArray(aa) {
				added, removed := scalarArrayDiff(ba, aa)
				var out []FieldDiff
				if len(removed) > 0 {
					out = append(out, FieldDiff{Path: path, Kind: "array_removed", Before: removed})
				}
				if len(added) > 0 {
					out = append(out, FieldDiff{Path: path, Kind: "array_added", After: added})
				}
				if len(out) == 0 {
					// Arrays differ but both scalar-added/removed sets empty
					// — happens when order changed. Fall through to raw diff.
					return []FieldDiff{{Path: path, Kind: "array", Before: before, After: after}}
				}
				return out
			}
			// Array of objects or mixed — opaque whole-array diff.
			return []FieldDiff{{Path: path, Kind: "array", Before: before, After: after}}
		}
	}
	// Scalar (or type mismatch) — emit a single entry.
	return []FieldDiff{{Path: path, Kind: "scalar", Before: before, After: after}}
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// isScalarArray returns true when every element of the slice is a JSON
// scalar (string, number, bool, nil). Arrays of objects are NOT scalar.
func isScalarArray(a []any) bool {
	for _, v := range a {
		switch v.(type) {
		case string, float64, bool, nil:
			continue
		default:
			return false
		}
	}
	return true
}

// scalarArrayDiff returns (added, removed) sets by comparing slices as
// multisets of scalar values. Output is sorted by string representation
// so the UI renders the same order across refreshes — map iteration is
// non-deterministic in Go, which would otherwise flicker the diff.
func scalarArrayDiff(before, after []any) (added, removed []any) {
	bCount := map[any]int{}
	for _, v := range before {
		bCount[v]++
	}
	aCount := map[any]int{}
	for _, v := range after {
		aCount[v]++
	}
	for v, n := range aCount {
		extra := n - bCount[v]
		for i := 0; i < extra; i++ {
			added = append(added, v)
		}
	}
	for v, n := range bCount {
		extra := n - aCount[v]
		for i := 0; i < extra; i++ {
			removed = append(removed, v)
		}
	}
	sortScalarSlice(added)
	sortScalarSlice(removed)
	return added, removed
}

// sortScalarSlice orders a slice of JSON scalars by their Go-stringified
// representation. Works for mixed-type slices (unlikely in practice, but
// defensive) — every value goes through fmt.Sprintf so the compare is
// type-agnostic.
func sortScalarSlice(s []any) {
	sort.SliceStable(s, func(i, j int) bool {
		return fmt.Sprintf("%v", s[i]) < fmt.Sprintf("%v", s[j])
	})
}

// sortResult orders each list by category then name so the UI is stable
// across refreshes.
func sortResult(r *RuleDiffResult) {
	sortSlice := func(s []RuleDiff) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].Category != s[j].Category {
				return s[i].Category < s[j].Category
			}
			return strings.ToLower(s[i].Name) < strings.ToLower(s[j].Name)
		})
	}
	sortSlice(r.Added)
	sortSlice(r.Modified)
	sortSlice(r.Removed)
}
