// Package customize — v2 op model.
//
// This file defines the on-disk and in-memory representation of customize
// operations. v2 replaces v0.5's RFC 6902 JSON Patch with a semantic-diff
// model that matches array elements by content-derived fingerprint rather
// than by integer index. See docs/customize-v2-design.md §4 for the design.
//
// All five Phase A scaffolding files (ops.go, fingerprint.go, match.go,
// conflict.go, migrate.go) compile against types only — no logic.
// Implementation lands in Phases B-F.

package customize

// OpKind enumerates the four primitive operations in the v2 model.
type OpKind string

const (
	// OpAdd inserts Value into the array at Path, positioned per Position.
	OpAdd OpKind = "add"

	// OpRemove deletes the element matched by Match from the array at Path.
	OpRemove OpKind = "remove"

	// OpReplace modifies Field on the element matched by Match within the
	// array at Path. When Match is nil, the parent at Path is treated as an
	// object and Field is the key to overwrite.
	OpReplace OpKind = "replace"

	// OpDescend recurses into the element matched by Match within the array
	// at Path, applying SubOps relative to that element. SubOps' Path is
	// relative to the matched element, not the document root.
	OpDescend OpKind = "descend"
)

// Op is a single semantic-diff operation. JSON-serialised one of:
//
//	{"kind":"add","path":[...],"position":{...},"value":...}
//	{"kind":"remove","path":[...],"match":{"fingerprint":"..."}}
//	{"kind":"replace","path":[...],"match":{"fingerprint":"..."},"field":"value","value":...}
//	{"kind":"descend","path":[...],"match":{"fingerprint":"..."},"sub_ops":[...]}
//
// Empty fields are omitted via omitempty so the on-disk form stays compact.
type Op struct {
	Kind OpKind   `json:"kind"`
	Path []string `json:"path"`

	// Add only.
	Position *Position `json:"position,omitempty"`

	// Add / Replace only.
	Value any `json:"value,omitempty"`

	// Remove / Replace / Descend only.
	Match *MatchSpec `json:"match,omitempty"`

	// Replace only.
	Field string `json:"field,omitempty"`

	// Replace only: the captured-at-time value, used by apply as an
	// implicit compare-and-swap when the op is an object-key Replace
	// (Match==nil). If upstream-now has a value at this key different
	// from OldValue, apply surfaces value_drifted instead of silently
	// overwriting maintainer's edit. Wrapped in a json.RawMessage-
	// shaped any so absent vs. present-but-null are distinguishable.
	// Array-element Replaces use Match.Fingerprint (which encodes the
	// old value) for the same purpose; OldValue is set only on
	// object-key Replaces.
	OldValue any `json:"old_value,omitempty"`

	// Descend only.
	SubOps []Op `json:"sub_ops,omitempty"`
}

// Position is where an OpAdd inserts its Value into an array.
//
// Kind = "start"  → index 0
// Kind = "end"    → len(arr)
// Kind = "after"  → index after the element matched by Anchor (+1)
// Kind = "before" → index of the element matched by Anchor
//
// When Anchor cannot be located at apply time, FallbackEnd (default true)
// causes the element to land at end-of-array with a low-severity conflict
// reported. Set FallbackEnd=false on ops where wrong-position is worse than
// not-applied; not currently used by capture (capture always emits true).
type Position struct {
	Kind        string      `json:"kind"`
	Anchor      Fingerprint `json:"anchor,omitempty"`
	FallbackEnd bool        `json:"fallback_end"`
}

// MatchSpec identifies an element within an array by its content
// fingerprint. Duplicate fingerprints within the same array are handled by
// closest-in-order pairing at apply time — see design §5.1 / Q4.
type MatchSpec struct {
	// Fingerprint is the FULL content fingerprint of the upstream-at-
	// capture-time element. Apply uses this for implicit compare-and-
	// swap: if upstream-now still has an element with this fp, target
	// is unchanged and apply proceeds.
	Fingerprint Fingerprint `json:"fingerprint"`

	// Partial (B3 fix from code review) is the leaf's PARTIAL
	// fingerprint at capture — same field+operator+negate, ignoring
	// value. When the full Fingerprint lookup misses at apply time
	// (because upstream-now has a different value at that field),
	// Partial lets us look up the drifted leaf precisely instead of
	// the previous probe-by-Field-existence heuristic, which
	// false-matched across operators. Set only on Replace ops that
	// target array-element leaves; absent ("") for Remove/Descend
	// (those rely on full Fingerprint alone).
	Partial Fingerprint `json:"partial,omitempty"`
}

// CustomizationV2 is the on-disk form of a (sub, slug) rule's user
// customizations in the v0.6+ format. v0.5 used a different on-disk
// shape (see Customization in storage.go) — Phase F migration archives
// those rather than converting.
//
// In Phase C, when v0.5 capture/apply code is replaced, the existing
// Customization type in storage.go is either renamed to CustomizationV1
// (kept for the brief migration window) or fully removed. Until then,
// new code uses CustomizationV2 to avoid colliding with the live v0.5
// type.
type CustomizationV2 struct {
	SchemaVersion       string `json:"schemaVersion"`                 // "2" for the v2 format
	RuleSchemaVersion   string `json:"ruleSchemaVersion"`             // Qui rule schema ("1" today)
	CapturedAt          string `json:"capturedAt"`                    // ISO 8601 UTC
	UpstreamFingerprint string `json:"upstreamFingerprint,omitempty"` // fp of upstream rule at capture
	Notes               string `json:"notes,omitempty"`               // user-supplied free text
	Ops                 []Op   `json:"ops"`
}

// CurrentSchemaVersion is what new captures write. Bump when ops semantics
// change in a backwards-incompatible way; pair with a migrator in
// migrate.go.
const CurrentSchemaVersion = "2"
