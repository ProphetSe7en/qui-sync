package customize

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wI2L/jsondiff"
)

// mustPatch decodes an RFC 6902 patch string into jsondiff.Patch for
// direct-transform tests. Fails the test on decode error rather than
// requiring every caller to error-handle.
func mustPatch(t *testing.T, s string) jsondiff.Patch {
	t.Helper()
	var p jsondiff.Patch
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("decode test patch: %v", err)
	}
	return p
}

// Sample upstream rule matching TRaSH's "Delete: noHL Tier 1" — the
// real-world rule the spec uses as its motivating example. Trimmed
// for test brevity; structure matches Qui's actual schema (verified
// during the data-model fact-check phase).
const ruleUpstream = `{
  "name": "Delete: noHL Tier 1",
  "trackerPattern": "*",
  "trackerDomains": ["*"],
  "enabled": true,
  "dryRun": false,
  "intervalSeconds": 21600,
  "sortOrder": 18,
  "conditions": {
    "schemaVersion": "1",
    "delete": {
      "enabled": true,
      "mode": "deleteWithFilesPreserveCrossSeeds",
      "condition": {
        "operator": "AND",
        "conditions": [
          {"field": "TAGS", "operator": "EQUAL", "value": "Tier1"},
          {"field": "TAGS", "operator": "EQUAL", "value": "noHL"}
        ]
      }
    }
  }
}`

// liveWithExtraCondition is the upstream rule + the user's added
// "TAGS NOT_EQUAL upload" condition — the exact use case the user
// described as "I add one condition to the Delete: noHL Tier 1 rule".
const liveWithExtraCondition = `{
  "name": "Delete: noHL Tier 1",
  "trackerPattern": "*",
  "trackerDomains": ["*"],
  "enabled": true,
  "dryRun": false,
  "intervalSeconds": 21600,
  "sortOrder": 18,
  "conditions": {
    "schemaVersion": "1",
    "delete": {
      "enabled": true,
      "mode": "deleteWithFilesPreserveCrossSeeds",
      "condition": {
        "operator": "AND",
        "conditions": [
          {"field": "TAGS", "operator": "EQUAL", "value": "Tier1"},
          {"field": "TAGS", "operator": "EQUAL", "value": "noHL"},
          {"field": "TAGS", "operator": "NOT_EQUAL", "value": "upload"}
        ]
      }
    }
  }
}`

// ---- Storage tests ----

func TestStorage_SaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	src := &Customization{
		SchemaVersionAssumed: "1",
		BaseSHA:              "sha256:test",
		CapturedFrom:         CapturedFromSetupTime,
		Diff:                 json.RawMessage(`[{"op":"add","path":"/x","value":1}]`),
		Notes:                "test note",
	}
	if err := Save(dir, "trash", "delete-nohl-tier1", src); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(dir, "trash", "delete-nohl-tier1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected loaded customization, got nil")
	}
	if loaded.Notes != "test note" {
		t.Errorf("notes diverged: %q", loaded.Notes)
	}
	if loaded.Version != FileFormatVersion {
		t.Errorf("version: got %d, want %d", loaded.Version, FileFormatVersion)
	}
	if loaded.CreatedAt.IsZero() {
		t.Error("CreatedAt should be auto-set by Save")
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be auto-set by Save")
	}
}

func TestStorage_MissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir, "trash", "nonexistent")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c != nil {
		t.Errorf("expected nil for missing file, got %+v", c)
	}
}

func TestStorage_UnsupportedVersionRejected(t *testing.T) {
	dir := t.TempDir()
	path := filePath(dir, "trash", "futurefile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	bogus := `{"version": 999, "schema_version_assumed": "1", "base_sha": "x", "diff": []}`
	if err := os.WriteFile(path, []byte(bogus), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, "trash", "futurefile")
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("expected version-mismatch error, got %v", err)
	}
}

func TestStorage_DeleteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "trash", "x", &Customization{Diff: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, "trash", "x"); err != nil {
		t.Errorf("first delete: %v", err)
	}
	if err := Delete(dir, "trash", "x"); err != nil {
		t.Errorf("second delete should be no-op: %v", err)
	}
}

func TestStorage_ListAcrossSubscriptions(t *testing.T) {
	dir := t.TempDir()
	_ = Save(dir, "trash", "a", &Customization{Diff: json.RawMessage(`[]`)})
	_ = Save(dir, "trash", "b", &Customization{Diff: json.RawMessage(`[]`)})
	_ = Save(dir, "other", "c", &Customization{Diff: json.RawMessage(`[]`)})
	refs, err := List(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %+v", len(refs), refs)
	}
}

func TestStorage_ListEmptyDirReturnsNil(t *testing.T) {
	dir := t.TempDir()
	refs, err := List(dir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected empty, got %d", len(refs))
	}
}

// ---- Capture tests ----

func TestCapture_AddConditionHappyPath(t *testing.T) {
	// The motivating use case: user adds one condition to the Delete
	// rule's inner AND-array. Capture should produce a diff that, when
	// applied to upstream, reproduces live.
	c, err := Capture([]byte(ruleUpstream), []byte(liveWithExtraCondition), CapturedFromSetupTime, "skip uploads", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil customization")
	}
	// Verify diff contains the added condition.
	if !strings.Contains(string(c.Diff), `"NOT_EQUAL"`) {
		t.Errorf("diff should contain the added condition, got: %s", c.Diff)
	}
	if !strings.Contains(string(c.Diff), `"upload"`) {
		t.Errorf("diff should contain the upload value, got: %s", c.Diff)
	}
	if c.SchemaVersionAssumed != "1" {
		t.Errorf("schemaVersion: got %q, want 1", c.SchemaVersionAssumed)
	}
	if c.Notes != "skip uploads" {
		t.Errorf("notes lost: %q", c.Notes)
	}
}

func TestCapture_IdenticalReturnsErrNoChanges(t *testing.T) {
	_, err := Capture([]byte(ruleUpstream), []byte(ruleUpstream), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if !errors.Is(err, ErrNoChanges) {
		t.Errorf("expected ErrNoChanges, got %v", err)
	}
}

func TestCapture_StripsAutoPreservedFields(t *testing.T) {
	// User flipped enabled false; with the auto-preserve strip ON
	// this should NOT appear in the diff (it'll be handled by Layer 3
	// at apply time instead).
	live := strings.Replace(ruleUpstream, `"enabled": true`, `"enabled": false`, 1)
	c, err := Capture([]byte(ruleUpstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if errors.Is(err, ErrNoChanges) {
		// Expected — the ONLY change is a stripped one, leaving an empty diff.
		return
	}
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(string(c.Diff), `"/enabled"`) {
		t.Errorf("diff should NOT contain /enabled (auto-preserved): %s", c.Diff)
	}
}

func TestCapture_StripDisabledKeepsField(t *testing.T) {
	// With strip OFF, the same change should appear in the diff.
	live := strings.Replace(ruleUpstream, `"enabled": true`, `"enabled": false`, 1)
	opts := DefaultCaptureOptions()
	opts.StripAutoPreserved = false
	c, err := Capture([]byte(ruleUpstream), []byte(live), CapturedFromSetupTime, "", opts)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(string(c.Diff), `"/enabled"`) {
		t.Errorf("diff SHOULD contain /enabled when strip is off: %s", c.Diff)
	}
}

func TestCapture_StripsSetEqualArrayReorderings_ReplaceOpCase(t *testing.T) {
	// Only the simplest case is handled in v1: jsondiff emits a single
	// `replace` op on the whole array. For more complex reorderings
	// jsondiff emits per-element add/remove sequences which the v1
	// strip doesn't recognise — see KnownLimitations in capture.go.
	//
	// This test pins the case the spec calls out (trackerDomains-style
	// noise). The harder per-element grouping is deferred to Phase 1.x
	// polish if/when we see real-world reorderings that don't get
	// caught by the v1 strip.
	upstream := `{"name":"r","conditions":{"schemaVersion":"1"},"customList":["a","b","c"]}`
	live := `{"name":"r","conditions":{"schemaVersion":"1"},"customList":["c","b","a"]}`
	c, err := Capture([]byte(upstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	// We accept either ErrNoChanges (strip worked) OR a non-empty diff
	// (jsondiff emitted per-element ops we can't recognise yet). Both
	// are correct behaviour for v1.
	if err != nil && !errors.Is(err, ErrNoChanges) {
		t.Fatalf("unexpected err: %v", err)
	}
	if err == nil && c != nil {
		// Got a non-empty diff — confirm it's per-element ops, not a
		// missed `replace`-op case (which WOULD be a bug).
		if strings.Contains(string(c.Diff), `"op":"replace","path":"/customList"`) {
			t.Errorf("strip missed a top-level replace op on a set-equal array: %s", c.Diff)
		}
	}
}

func TestCapture_TrailingAppendUsesEndOfArrayMarker(t *testing.T) {
	// The motivating use case: user added a condition at the end of
	// an existing AND-array. Diff should use the RFC 6902 end-of-array
	// marker `/-` so the patch survives upstream array reshuffles.
	//
	// jsondiff happens to emit `/-` for trailing appends already, so
	// our rewriteAppendsToTrailingMarker is currently a defensive
	// no-op for this path (proven by TestRewriteAppendsToTrailingMarker
	// below which exercises the transform with a hand-built positional
	// patch). Both paths converge on the same correct output.
	c, err := Capture([]byte(ruleUpstream), []byte(liveWithExtraCondition), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(string(c.Diff), `/-`) {
		t.Errorf("expected trailing-marker path '/-' in diff for trailing append; got: %s", c.Diff)
	}
	if strings.Contains(string(c.Diff), `/conditions/2"`) || strings.Contains(string(c.Diff), `/conditions/2/`) {
		t.Errorf("diff should not retain positional /conditions/2 path: %s", c.Diff)
	}
}

func TestRewriteAppendsToTrailingMarker_Direct(t *testing.T) {
	// Direct test on the transform — feed a hand-built patch with a
	// positional append at the end-of-array index, verify it gets
	// rewritten to /-. Guards against jsondiff future-changing its
	// default output to emit positional indices (where we'd need our
	// rewrite to step in).
	upstream := []byte(`{"items":["a","b"]}`)
	patch := mustPatch(t, `[{"op":"add","path":"/items/2","value":"c"}]`)
	rewritten := rewriteAppendsToTrailingMarker(patch, upstream)
	if rewritten[0].Path != "/items/-" {
		t.Errorf("expected /items/-, got %q", rewritten[0].Path)
	}
}

func TestRewriteAppendsToTrailingMarker_LeavesMidInsertAlone(t *testing.T) {
	// Insert at the MIDDLE of an array (index 1 in a 2-element array)
	// is semantically different from append-to-end — must NOT rewrite
	// to `/-`, that would change the meaning.
	upstream := []byte(`{"items":["a","b"]}`)
	patch := mustPatch(t, `[{"op":"add","path":"/items/1","value":"c"}]`)
	rewritten := rewriteAppendsToTrailingMarker(patch, upstream)
	if rewritten[0].Path != "/items/1" {
		t.Errorf("mid-insert should NOT be rewritten, got %q", rewritten[0].Path)
	}
}

func TestCapture_ExtractsSchemaVersion(t *testing.T) {
	upstream := `{"name":"r","conditions":{"schemaVersion":"42","items":[]}}`
	live := `{"name":"r","conditions":{"schemaVersion":"42","items":["new"]}}`
	c, err := Capture([]byte(upstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.SchemaVersionAssumed != "42" {
		t.Errorf("schemaVersion: got %q, want 42", c.SchemaVersionAssumed)
	}
}

func TestCapture_FallsBackToSchemaVersion1WhenMissing(t *testing.T) {
	upstream := `{"name":"r","x":1}`
	live := `{"name":"r","x":2}`
	c, err := Capture([]byte(upstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.SchemaVersionAssumed != "1" {
		t.Errorf("expected fallback to '1', got %q", c.SchemaVersionAssumed)
	}
}

// ---- Apply tests ----

func TestApply_HappyPath(t *testing.T) {
	// Capture against upstream → apply to upstream → result should
	// equal live.
	c, err := Capture([]byte(ruleUpstream), []byte(liveWithExtraCondition), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	result, err := Apply([]byte(ruleUpstream), c)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Conflict != nil {
		t.Fatalf("unexpected conflict: %+v", result.Conflict)
	}
	if len(result.Effective) == 0 {
		t.Fatal("expected effective rule")
	}
	// Canonical compare with live.
	canonEff, _ := canonicalize(result.Effective)
	canonLive, _ := canonicalize([]byte(liveWithExtraCondition))
	if sha256Hex(canonEff) != sha256Hex(canonLive) {
		t.Errorf("apply result does not match live:\ngot:  %s\nwant: %s", canonEff, canonLive)
	}
}

func TestApply_SchemaBumpConflict(t *testing.T) {
	c := &Customization{
		Version:              FileFormatVersion,
		SchemaVersionAssumed: "1",
		Diff:                 json.RawMessage(`[{"op":"add","path":"/foo","value":"bar"}]`),
	}
	// Upstream now claims schemaVersion "2" — should conflict.
	upstream := `{"conditions":{"schemaVersion":"2"}}`
	result, err := Apply([]byte(upstream), c)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.Conflict == nil {
		t.Fatal("expected conflict for schema bump")
	}
	if result.Conflict.Kind != ConflictSchemaBump {
		t.Errorf("kind: got %q, want %q", result.Conflict.Kind, ConflictSchemaBump)
	}
	if result.Effective != nil {
		t.Error("conflict should NOT populate Effective")
	}
}

func TestApply_RestructuredUpstreamProducesConflict(t *testing.T) {
	// Capture against original upstream → upstream gets restructured
	// (we'll simulate by removing the conditions array our diff
	// targets) → apply should produce a conflict, not an error.
	c, err := Capture([]byte(ruleUpstream), []byte(liveWithExtraCondition), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	// Pathological new upstream: the delete.condition object is gone.
	restructured := `{
  "name": "Delete: noHL Tier 1",
  "trackerPattern": "*",
  "conditions": {
    "schemaVersion": "1",
    "delete": {
      "enabled": true,
      "mode": "deleteWithFilesPreserveCrossSeeds"
    }
  }
}`
	result, err := Apply([]byte(restructured), c)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.Conflict == nil {
		t.Fatal("expected conflict for restructured upstream")
	}
	if result.Conflict.Kind != ConflictPatchFailed {
		t.Errorf("kind: got %q, want %q", result.Conflict.Kind, ConflictPatchFailed)
	}
	if result.Conflict.PatchErr == "" {
		t.Error("expected PatchErr populated with underlying library message")
	}
}

func TestApply_InvalidDiffJSONReturnsError(t *testing.T) {
	c := &Customization{
		Version:              FileFormatVersion,
		SchemaVersionAssumed: "1",
		Diff:                 json.RawMessage(`not valid json`),
	}
	upstream := `{"conditions":{"schemaVersion":"1"}}`
	_, err := Apply([]byte(upstream), c)
	if err == nil {
		t.Error("expected error for invalid diff JSON")
	}
}

func TestApply_NilCustomizationReturnsError(t *testing.T) {
	_, err := Apply([]byte(`{}`), nil)
	if err == nil {
		t.Error("expected error for nil customization")
	}
}

// ---- Cross-package consistency ----

// ---- Code-review-finding regression tests ----

// TestStripAutoPreserved_NestedPathsStripped is the regression test
// for review finding C4 — pre-fix, the top-level-only match silently
// let `/trackerDomains/0` slip through, and Layer 3 would then
// wholesale-replace the array at apply time, losing the user's edit.
func TestStripAutoPreserved_NestedPathsStripped(t *testing.T) {
	upstream := `{"trackerDomains":["a","b"],"otherField":"x"}`
	live := `{"trackerDomains":["a","BBB"],"otherField":"x"}`
	_, err := Capture([]byte(upstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	// The only diff op targets /trackerDomains/1 (replace) — a nested
	// path under an auto-preserved field. Post-fix, that should be
	// stripped, leaving an empty diff → ErrNoChanges.
	if !errors.Is(err, ErrNoChanges) {
		t.Errorf("nested auto-preserved edit should produce ErrNoChanges (Layer 3 owns it), got %v", err)
	}
}

// TestTransformsDoNotMutateCaller verifies the slice-aliasing fix
// (review B2 + C5). Transforms used to do `out := patch[:0]` which
// aliased the caller's backing array; post-fix they allocate fresh
// slices so the caller's input is preserved across the call.
func TestTransformsDoNotMutateCaller(t *testing.T) {
	original := mustPatch(t, `[{"op":"replace","path":"/enabled","value":false},{"op":"add","path":"/conditions/-","value":1}]`)
	captured := make(jsondiff.Patch, len(original))
	copy(captured, original)

	_ = stripAutoPreserved(original)
	if len(original) != len(captured) || original[0].Path != captured[0].Path || original[1].Path != captured[1].Path {
		t.Errorf("stripAutoPreserved mutated caller's slice")
	}

	_ = rewriteAppendsToTrailingMarker(original, []byte(`{"conditions":[]}`))
	if original[1].Path != captured[1].Path {
		t.Errorf("rewriteAppendsToTrailingMarker mutated caller's slice")
	}
}

// TestExtractSchemaVersion_NumericValueNotMaskedAsOne is the regression
// for review B3+C2. Pre-fix, `schemaVersion: 2` (numeric) Unmarshal'd
// into a string field and silently fell back to "1", masking a real
// upstream schema bump.
func TestExtractSchemaVersion_NumericValueNotMaskedAsOne(t *testing.T) {
	upstream := []byte(`{"conditions":{"schemaVersion":2}}`)
	got := extractSchemaVersion(upstream)
	if got == "1" {
		t.Errorf("numeric schemaVersion should NOT fall back to '1' (masks schema bump); got %q", got)
	}
}

// TestExtractSchemaVersion_StringFormCommonCase keeps the happy-path
// behaviour for the standard `"schemaVersion": "1"` shape.
func TestExtractSchemaVersion_StringFormCommonCase(t *testing.T) {
	upstream := []byte(`{"conditions":{"schemaVersion":"1"}}`)
	if got := extractSchemaVersion(upstream); got != "1" {
		t.Errorf("got %q, want 1", got)
	}
}

// TestCapture_PopulatesFragileOps covers review finding C3 — the
// FragileOps field surfaces positional ops so the UI can warn the
// user when their customization might break under upstream restructure.
func TestCapture_PopulatesFragileOps(t *testing.T) {
	// Edit an existing condition's value in-place — produces a
	// positional `replace` op at `/conditions/delete/condition/conditions/0/value`.
	live := strings.Replace(ruleUpstream, `"Tier1"`, `"TIER1_MODIFIED"`, 1)
	c, err := Capture([]byte(ruleUpstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(c.FragileOps) == 0 {
		t.Errorf("expected FragileOps populated for positional replace; got empty")
	}
	for _, p := range c.FragileOps {
		if !strings.Contains(p, "/conditions/") {
			t.Errorf("unexpected fragile path %q", p)
		}
	}
}

// TestCapture_RobustOpsLeaveFragileOpsEmpty — the motivating use case
// (add condition to end of AND-array) should NOT flag anything as
// fragile. The captured op uses /- (idempotent end marker), which is
// robust per spec.
func TestCapture_RobustOpsLeaveFragileOpsEmpty(t *testing.T) {
	c, err := Capture([]byte(ruleUpstream), []byte(liveWithExtraCondition), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(c.FragileOps) != 0 {
		t.Errorf("expected zero fragile ops for end-of-array append; got %v", c.FragileOps)
	}
}

// TestHasPositionalSegment_Cases unit-tests the classifier directly
// so the matrix in scanFragileOps's doc-comment stays accurate.
func TestHasPositionalSegment_Cases(t *testing.T) {
	cases := map[string]bool{
		"":                                    false,
		"/enabled":                            false, // top-level scalar
		"/conditions/-":                       false, // end-of-array marker is robust
		"/conditions/0":                       true,  // positional element
		"/conditions/delete/condition/conditions/2/value": true, // deep positional
		"/object/newField":                    false, // adding a key
		"/object/nested/key":                  false, // all-named path
	}
	for path, want := range cases {
		got := hasPositionalSegment(path)
		if got != want {
			t.Errorf("hasPositionalSegment(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestAutoPreservedMatchesCore moved to core/customize_consistency_test.go
// to break an import cycle: customize is imported by core/sync.go for
// the apply-pipeline integration, so customize_test cannot import core.
// The consistency assertion lives in core's test suite and reads
// customize.AutoPreservedFields().

// ---- Noise-field normalization (Phase 1.3 smoke-test finding) ----

// TestCapture_StripsServerAssignedMetadataFromDiff is the regression
// test for the in-browser smoke-test finding where every captured
// customization contained 5+ noise ops (id, instanceId, createdAt,
// updatedAt from live; _slug from upstream) drowning the actual
// user changes. Post-fix, Capture normalises both inputs by removing
// these fields before computing the diff, so the resulting patch
// only contains the meaningful structural changes.
func TestCapture_StripsServerAssignedMetadataFromDiff(t *testing.T) {
	// Realistic shape: upstream is what the maintainer published
	// (has _slug, no Qui-assigned fields); live is what Qui's API
	// returned (no _slug, has Qui-assigned fields). The ONLY
	// meaningful difference is the added category condition.
	upstream := `{
		"_slug": "tag-tier1",
		"name": "Tag: Tier 1",
		"trackerPattern": "*",
		"conditions": {"schemaVersion": "1"}
	}`
	live := `{
		"id": 114,
		"instanceId": 1,
		"createdAt": "2026-05-11T20:43:41Z",
		"updatedAt": "2026-05-24T21:09:09Z",
		"name": "Tag: Tier 1",
		"trackerPattern": "*",
		"conditions": {
			"schemaVersion": "1",
			"category": {"category": "music", "enabled": true}
		}
	}`
	c, err := Capture([]byte(upstream), []byte(live), CapturedFromSetupTime, "", DefaultCaptureOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	diffStr := string(c.Diff)

	// The only op should be the category addition; explicitly assert
	// the noise fields are NOT present.
	for _, noise := range []string{"/_slug", "/id", "/instanceId", "/createdAt", "/updatedAt"} {
		if strings.Contains(diffStr, `"`+noise+`"`) {
			t.Errorf("noise field %s should be stripped from diff; got: %s", noise, diffStr)
		}
	}
	// The real change must survive.
	if !strings.Contains(diffStr, "category") {
		t.Errorf("real user change should appear in diff; got: %s", diffStr)
	}
}

// TestStripFields_RemovesTopLevelOnly proves the helper only touches
// top-level fields (nested occurrences of the same name survive).
// Matters because qui-sync rules may legitimately use names like
// "id" or "name" inside nested condition objects.
func TestStripFields_RemovesTopLevelOnly(t *testing.T) {
	in := []byte(`{"id": 42, "conditions": {"id": "nested-id-stays"}}`)
	out, err := stripFields(in, []string{"id"})
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	s := string(out)
	if strings.Contains(s, `"id":42`) || strings.Contains(s, `"id": 42`) {
		t.Errorf("top-level id should be stripped: %s", s)
	}
	if !strings.Contains(s, "nested-id-stays") {
		t.Errorf("nested id should survive: %s", s)
	}
}

// ---- Storage identifier validation (review B2) ----

func TestValidIdentifier_Cases(t *testing.T) {
	cases := map[string]bool{
		"delete-nohl-tier1": true,
		"trash":             true,
		"abc123":            true,
		"a":                 true,
		"_internal":         true, // underscore is allowed (alnum-like; harmless prefix)
		"":                  false,
		".":                 false,
		"..":                false,
		"-leading-dash":     false,
		".hidden":           false,
		"a/b":               false,
		`a\b`:               false,
		"a..b":              false,
		"../../etc/passwd":  false,
		"NUL\x00byte":       false,
		"emoji-🦀":           false,
	}
	for s, want := range cases {
		if got := validIdentifier(s); got != want {
			t.Errorf("validIdentifier(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestSave_RejectsTraversalSlug(t *testing.T) {
	dir := t.TempDir()
	c := &Customization{Diff: json.RawMessage(`[]`)}
	err := Save(dir, "trash", "../../tmp/pwned", c)
	if err == nil {
		t.Fatal("expected ErrInvalidIdentifier for ../-traversal slug")
	}
	if !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("expected ErrInvalidIdentifier, got %v", err)
	}
}

func TestLoad_RejectsTraversalSlug(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir, "trash", "../../etc/passwd")
	if !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("expected ErrInvalidIdentifier, got %v", err)
	}
}

func TestDelete_RejectsTraversalSub(t *testing.T) {
	dir := t.TempDir()
	err := Delete(dir, "../escape", "slug")
	if !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("expected ErrInvalidIdentifier, got %v", err)
	}
}
