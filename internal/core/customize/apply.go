package customize

import (
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

// ApplyResult is the outcome of applying a customization to a fresh
// upstream rule. Either Effective is populated (apply succeeded — push
// this to Qui), or Conflict is populated (apply failed — show conflict
// UI to the user, do NOT push). Exactly one of the two is set on a
// non-error return — enforced by the invariant check in Apply (review
// finding C8).
type ApplyResult struct {
	Effective []byte
	Conflict  *Conflict
}

// invariant returns an error when the result violates the "exactly one
// of Effective / Conflict is set" contract. Called from Apply right
// before return as belt-and-braces against future code paths that
// forget to populate one or the other.
func (r *ApplyResult) invariant() error {
	hasEffective := len(r.Effective) > 0
	hasConflict := r.Conflict != nil
	if hasEffective && hasConflict {
		return fmt.Errorf("ApplyResult invariant violated: both Effective and Conflict are set")
	}
	if !hasEffective && !hasConflict {
		return fmt.Errorf("ApplyResult invariant violated: neither Effective nor Conflict is set")
	}
	return nil
}

// Conflict describes why a customization couldn't be applied to a new
// upstream rule. Surfaced in the UI's conflict-resolution modal so the
// user has actionable info (what changed upstream, what they had
// customized, what the apply error said).
type Conflict struct {
	// Reason is a human-readable summary suitable for direct UI
	// display ("schema bumped: '1' → '2'" / "diff path missing in
	// upstream: /conditions/delete/condition/conditions/-").
	Reason string `json:"reason"`
	// Kind is a stable enum for programmatic branching in the UI
	// (e.g. show different help text for schema-bump vs path-missing
	// vs roundtrip-mismatch).
	Kind ConflictKind `json:"kind"`
	// PatchErr is the underlying error message from the JSON Patch
	// library, when applicable. Empty for schema-bump conflicts.
	PatchErr string `json:"patch_err,omitempty"`
}

// ConflictKind enumerates the conflict categories the apply pipeline
// produces. Stable strings — UI keys off these for help-copy lookup.
type ConflictKind string

const (
	// ConflictSchemaBump fires when upstream's conditions.schemaVersion
	// differs from the customization's schema_version_assumed. We
	// refuse to apply (the rule's structure may have changed in ways
	// the diff can't safely navigate) — user must Re-capture or Drop.
	ConflictSchemaBump ConflictKind = "schema_bump"
	// ConflictPatchFailed fires when jsonpatch.Apply returns an error
	// (path missing, type mismatch, etc.) — upstream restructured in
	// a way the captured diff can't navigate.
	ConflictPatchFailed ConflictKind = "patch_failed"
)

// Apply takes a fresh upstream rule + a stored customization and
// returns the effective rule that should be PUT to Qui (after Layer 3
// field preservation, which is the caller's responsibility — see
// ApplyOptions documentation in the spec for the full three-layer
// pipeline ordering).
//
// upstream and c must be non-nil. Returns an error only for invalid
// inputs (nil customization, malformed diff JSON); upstream-structure
// mismatches surface as ApplyResult.Conflict, not as errors, so the
// caller can branch cleanly between "push to Qui" and "show conflict
// UI" without parsing error strings.
func Apply(upstream []byte, c *Customization) (*ApplyResult, error) {
	if len(upstream) == 0 {
		return nil, fmt.Errorf("upstream is empty")
	}
	if c == nil {
		return nil, fmt.Errorf("customization is nil")
	}

	// Schema-version gate. Per spec: when upstream's schemaVersion
	// changes, we refuse to apply and require user re-capture. This
	// is the conservative safe-by-default choice — better than
	// silently misapplying a diff against a restructured rule.
	upstreamSchema := extractSchemaVersion(upstream)
	if upstreamSchema != c.SchemaVersionAssumed {
		return &ApplyResult{
			Conflict: &Conflict{
				Kind:   ConflictSchemaBump,
				Reason: fmt.Sprintf("upstream schemaVersion changed: %q → %q", c.SchemaVersionAssumed, upstreamSchema),
			},
		}, nil
	}

	// Decode + apply the patch.
	patch, err := jsonpatch.DecodePatch(c.Diff)
	if err != nil {
		return nil, fmt.Errorf("decode diff: %w", err)
	}
	effective, err := patch.Apply(upstream)
	if err != nil {
		// jsonpatch returns "missing element" / "type mismatch" /
		// "out of bounds" depending on the failure mode. We surface
		// the raw library message so the UI can show "diff failed:
		// <reason>" — sufficient for the user to recognise which
		// upstream change broke their customization.
		return &ApplyResult{
			Conflict: &Conflict{
				Kind:     ConflictPatchFailed,
				Reason:   "upstream changed in a way that breaks your customization",
				PatchErr: err.Error(),
			},
		}, nil
	}

	// Sanity check — make sure what we produced is still valid JSON.
	// jsonpatch should always produce valid JSON, but a defensive
	// re-parse catches any future library regressions before we PUT
	// garbage to Qui.
	var probe interface{}
	if err := json.Unmarshal(effective, &probe); err != nil {
		return nil, fmt.Errorf("apply produced invalid JSON: %w", err)
	}

	result := &ApplyResult{Effective: effective}
	if err := result.invariant(); err != nil {
		return nil, err
	}
	return result, nil
}
