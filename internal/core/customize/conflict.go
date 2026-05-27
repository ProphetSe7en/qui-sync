// conflict.go — conflict types surfaced by SemanticApply.
//
// Every op can produce zero or one OpConflict. Severity determines UI
// behaviour (silent log / banner / modal / blocked apply). See design §5.4.
//
// Phase A: types only — values are populated by SemanticApply (Phase D).

package customize

// ConflictReason is the machine-readable category of an apply-time conflict.
type ConflictReason string

const (
	// ConflictAnchorGone: an OpAdd's Position.Anchor doesn't exist in the
	// current array. Default action: fall back to end-of-array, emit a
	// low-severity notification.
	ConflictAnchorGone ConflictReason = "anchor_gone"

	// ConflictTargetGone: a Remove/Replace/Descend's Match.Fingerprint
	// doesn't exist in the current array. Severity depends on op kind —
	// Remove is low (the user wanted it gone, it's gone), Replace and
	// Descend are medium (user wanted to modify something that no longer
	// exists).
	ConflictTargetGone ConflictReason = "target_gone"

	// ConflictValueDrifted: an OpReplace's target was not found by full
	// fingerprint, but a leaf with the same partial fingerprint (same
	// field+operator+negate, different value) IS present. The maintainer
	// changed the same field the user wanted to change. Surface both
	// values to the user — pick yours, theirs, or re-capture.
	ConflictValueDrifted ConflictReason = "value_drifted"

	// ConflictTypeChanged: the structure at Path is fundamentally
	// different (e.g. used to be an array, is now an object). Apply
	// blocks for this rule; user must re-capture or drop customize.
	ConflictTypeChanged ConflictReason = "type_changed"

	// ConflictPathGone: the Path itself can't be walked because an
	// intermediate object key has disappeared. Apply blocks.
	ConflictPathGone ConflictReason = "path_gone"

	// ConflictAmbiguous: multiple elements with the same fingerprint in
	// the same array. Apply uses the first match. Info severity (silent
	// log) — duplicates in user data are unusual but legal.
	ConflictAmbiguous ConflictReason = "ambiguous_target"
)

// ConflictSeverity drives UI surfacing behaviour.
type ConflictSeverity string

const (
	// SeverityInfo: silent log only. No UI surface.
	SeverityInfo ConflictSeverity = "info"

	// SeverityLow: notification banner ("your customize was applied with
	// minor degradation, FYI"). Dismissible. No user action required.
	SeverityLow ConflictSeverity = "low"

	// SeverityMedium: conflict modal with action choices (drop op /
	// re-capture / leave). User decides what to do next sync.
	SeverityMedium ConflictSeverity = "medium"

	// SeverityHigh: conflict modal that BLOCKS apply for this rule.
	// User MUST resolve before next sync proceeds.
	SeverityHigh ConflictSeverity = "high"
)

// OpConflict is a single conflict produced by SemanticApply. Multiple
// conflicts can accumulate from a single apply pass — UI groups them by
// rule and shows them together in the Conflict modal.
//
// SuggestedValue is populated for ConflictValueDrifted (the upstream-now
// value, so the modal can show "yours: X, theirs: Y"). Empty otherwise.
type OpConflict struct {
	Op             Op               `json:"op"`
	Reason         ConflictReason   `json:"reason"`
	Severity       ConflictSeverity `json:"severity"`
	Message        string           `json:"message"`
	SuggestedValue any              `json:"suggested_value,omitempty"`
}

// severityFor returns the default severity for (reason, opKind) pairs.
// Most reasons have a fixed severity; ConflictTargetGone varies by op
// kind (low on Remove, medium on Replace/Descend).
//
// Phase A: stub. Real mapping lands in Phase D alongside the apply path.
func severityFor(reason ConflictReason, opKind OpKind) ConflictSeverity {
	// TODO Phase D.
	return SeverityMedium
}
