package customize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/wI2L/jsondiff"
)

// noiseFields are present on one side of an upstream/live comparison
// but not the other — purely as a wire-format artifact, never as a
// user-meaningful difference. Stripped from BOTH sides before diff
// computation so they don't drown the real changes:
//
//   id, instanceId, createdAt, updatedAt
//     — Qui server-assigned metadata; present on live (from Qui API),
//       absent on upstream (maintainer's export drops them per
//       core.buildUpdatePayload's exclusion list).
//   _slug
//     — qui-sync's stable identifier embedded by the maintainer-side
//       export. Present on upstream (read from disk), absent on live
//       (Qui doesn't know about it).
//   _description
//     — maintainer's note for a published rule; not user-meaningful
//       once subscribed. Mirrored from buildUpdatePayload's strip list.
//
// Note this is DISTINCT from autoPreservedFields below: those are
// user-managed and Layer 3 cares about them. These are framework
// artifacts that no layer cares about.
var noiseFields = []string{
	"id",
	"instanceId",
	"_slug",
	"_description",
	"createdAt",
	"updatedAt",
}

// autoPreservedFields mirrors core.DefaultPreservedFields() — top-level
// scalar fields that Layer 3 of the apply pipeline always merges from
// live Qui state. Customizations MUST NOT include diff ops against
// these paths: doing so would let a captured diff overrule the
// preserve mechanism, which violates the "auto-fields always win"
// invariant in the spec.
//
// Defined here (not imported from core) to avoid an import cycle —
// customize is called BY core packages, not the other way around.
// Kept in sync with core.DefaultPreservedFields(); the
// TestAutoPreservedMatchesCore test in core/customize_consistency_test.go
// (deliberately in the core package, since core imports customize but
// not vice-versa) asserts the two stay aligned.
var autoPreservedFields = []string{
	"trackerPattern",
	"trackerDomains",
	"intervalSeconds",
	"freeSpaceSource",
	"enabled",
	"dryRun",
	"notify",
}

// AutoPreservedFields returns a copy of the auto-preserved field list
// so external packages (like the consistency test in core) can verify
// it against core.DefaultPreservedFields() without taking a reference
// to the underlying slice.
func AutoPreservedFields() []string {
	out := make([]string, len(autoPreservedFields))
	copy(out, autoPreservedFields)
	return out
}

// CaptureOptions tunes what Capture does to the raw diff before
// persisting. Zero-value is the recommended setting for production
// use (strip auto-preserved, rewrite trailing appends to "-"); tests
// flip individual knobs to assert each transform independently.
type CaptureOptions struct {
	// StripAutoPreserved removes diff ops whose path targets a field
	// in autoPreservedFields. ON in production; OFF in tests that
	// verify the unfiltered diff.
	StripAutoPreserved bool
	// RewriteAppendsToTrailingMarker converts `add /path/N` where N
	// equals the current length of the array at /path into the
	// idempotent `add /path/-` form. ON in production; OFF in tests
	// that verify the raw diff.
	RewriteAppendsToTrailingMarker bool
	// StripSetEqualArrayReorderings drops `replace` ops on arrays
	// whose old and new sets are equal (same elements, different
	// order). Used to ignore trackerDomains ordering noise. ON in
	// production.
	StripSetEqualArrayReorderings bool
}

// DefaultCaptureOptions returns the production-recommended option set
// (all three transforms ON). Callers should use this unless they have
// a specific reason to override (e.g. test introspection).
func DefaultCaptureOptions() CaptureOptions {
	return CaptureOptions{
		StripAutoPreserved:             true,
		RewriteAppendsToTrailingMarker: true,
		StripSetEqualArrayReorderings:  true,
	}
}

// Capture computes the diff between an upstream rule and the user's
// locally-edited Qui rule, applies the configured transformations,
// runs a roundtrip-validate check, and returns a populated
// Customization ready for Save().
//
// upstream and live must be canonical JSON of the SAME rule (same name,
// same shape — caller is responsible for matching). schema_version_assumed
// is read from upstream's conditions.schemaVersion if present; absence
// is treated as "1" (Qui's current schema).
//
// Returns ErrNoChanges when (after transforms) the diff is empty — the
// caller can treat that as "the user's Qui rule matches upstream
// already, nothing to customize". Returns a wrapped error if roundtrip
// validation fails (the computed diff doesn't actually produce live
// when applied to upstream — defensive against diff-library bugs +
// non-canonical JSON edge cases).
func Capture(upstream, live []byte, capturedFrom CapturedFrom, notes string, opts CaptureOptions) (*Customization, error) {
	if len(upstream) == 0 {
		return nil, fmt.Errorf("upstream is empty")
	}
	if len(live) == 0 {
		return nil, fmt.Errorf("live is empty")
	}

	// Pre-strip noise (server-assigned metadata + maintainer-only
	// fields) from BOTH sides before diffing. Without this, every
	// captured customization shows 5+ ops for id/createdAt/updatedAt/
	// instanceId/_slug, drowning the actual user changes. See
	// noiseFields docs for the rationale per-field.
	upstreamNorm, err := stripFields(upstream, noiseFields)
	if err != nil {
		return nil, fmt.Errorf("normalize upstream: %w", err)
	}
	liveNorm, err := stripFields(live, noiseFields)
	if err != nil {
		return nil, fmt.Errorf("normalize live: %w", err)
	}

	// Compute raw RFC 6902 patch upstream → live (post-normalisation).
	rawPatch, err := jsondiff.CompareJSON(upstreamNorm, liveNorm)
	if err != nil {
		return nil, fmt.Errorf("compute diff: %w", err)
	}

	// Roundtrip-validate the RAW diff FIRST, before any transform
	// touches it. Proves jsondiff produced a correct upstream → live
	// patch. We validate against the NORMALIZED versions because
	// that's what the diff was computed against — applying the patch
	// to upstreamNorm must reproduce liveNorm exactly.
	if err := roundtripValidate(upstreamNorm, liveNorm, rawPatch); err != nil {
		return nil, fmt.Errorf("roundtrip validate raw patch: %w", err)
	}

	// Apply transforms. Order matters: strip set-equal reorderings
	// FIRST (cheap, may zero out the rest), then strip auto-preserved
	// (may zero out structural noise), then rewrite trailing appends
	// (cosmetic / robustness improvement on what's left).
	//
	// Transforms reference the NORMALIZED upstream/live (the same
	// inputs the diff was computed against). Using raw upstream here
	// would risk a length mismatch for arrays whose noise siblings
	// got stripped — the trailing-marker rewrite would aim at the
	// wrong end-of-array index.
	patch := rawPatch
	if opts.StripSetEqualArrayReorderings {
		patch = stripSetEqualArrayReorderings(patch, upstreamNorm, liveNorm)
	}
	if opts.StripAutoPreserved {
		patch = stripAutoPreserved(patch)
	}
	if opts.RewriteAppendsToTrailingMarker {
		patch = rewriteAppendsToTrailingMarker(patch, upstreamNorm)
	}

	// Serialise the transformed patch.
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("marshal patch: %w", err)
	}

	// Empty-diff case — the caller may want to know this rather than
	// persist a no-op customization. Returning ErrNoChanges lets them
	// branch cleanly without parsing the diff payload.
	if isEmptyDiff(patchJSON) {
		return nil, ErrNoChanges
	}

	// Post-transform validation: the transformed patch must at least
	// DECODE + APPLY cleanly to upstream. We don't assert equality
	// with live (transforms intentionally strip ops that touch
	// auto-preserved fields, so transformed apply won't reproduce
	// live exactly — only the customize-relevant subset). But the
	// transformed patch must remain a valid, applicable patch, or
	// we have a transform bug. Review finding B1.
	if err := transformedPatchApplies(upstream, patchJSON); err != nil {
		return nil, fmt.Errorf("post-transform validate: %w", err)
	}

	// Pull the schema version out of upstream so future schema bumps
	// trigger a forced review per the spec's conflict matrix.
	schemaVer := extractSchemaVersion(upstream)
	now := time.Now().UTC()

	return &Customization{
		Version:              FileFormatVersion,
		SchemaVersionAssumed: schemaVer,
		BaseSHA:              sha256Hex(upstream),
		CreatedAt:            now,
		UpdatedAt:            now,
		CapturedFrom:         capturedFrom,
		Diff:                 patchJSON,
		Notes:                strings.TrimSpace(notes),
		FragileOps:           scanFragileOps(patch),
	}, nil
}

// scanFragileOps returns the paths of ops whose semantics depend on a
// specific position in an array — i.e. any op whose path has a
// numeric segment that ISN'T the RFC 6902 end-of-array marker `/-`.
// These ops break the moment upstream inserts/removes a sibling at
// any position before them.
//
// Robust ops (NOT flagged):
//   - add /path/-           — end-of-array marker, survives reorders
//   - replace /scalar       — top-level scalar replace
//   - add /object/newField  — adding a key to an object
//
// Fragile ops (flagged):
//   - replace /path/0/value — positional element replace
//   - remove /path/3        — positional remove
//   - add /path/1           — positional insert (mid-array)
//
// Returns nil (not empty slice) when no fragile ops are present so
// the JSON omitempty hint works as expected.
func scanFragileOps(patch jsondiff.Patch) []string {
	var fragile []string
	for _, op := range patch {
		if hasPositionalSegment(op.Path) {
			fragile = append(fragile, op.Path)
		}
	}
	return fragile
}

// hasPositionalSegment reports whether any segment of a JSON Pointer
// is a numeric array index (not `-`). Used by scanFragileOps to
// classify diff ops as positional / robust.
func hasPositionalSegment(path string) bool {
	if path == "" {
		return false
	}
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if seg == "" || seg == "-" {
			continue
		}
		if _, err := strconv.Atoi(seg); err == nil {
			return true
		}
	}
	return false
}

// ErrNoChanges is returned by Capture when the (post-transform) diff
// is empty — meaningful UX signal so the caller can show "no
// customization needed, your rule matches upstream" rather than
// persisting a no-op file.
var ErrNoChanges = fmt.Errorf("no semantic changes between upstream and live")

// ---- transforms ----

// stripAutoPreserved drops every op whose path targets — at any depth —
// a field in autoPreservedFields. Both exact top-level matches
// (`/enabled`) and nested element edits (`/trackerDomains/0`,
// `/freeSpaceSource/path`) are stripped, because Layer 3 owns the
// whole subtree under each preserved field.
//
// Pre-fix this was top-level only, which silently let a "change a
// single element of trackerDomains" customization slip through; Layer 3
// would then wholesale-replace the array at apply time and lose the
// user's edit. See code-review finding C4.
//
// Allocates a fresh slice — never mutates the caller's input. The
// previous `patch[:0]` form aliased the caller's backing array and
// corrupted later validation passes (review finding B2).
func stripAutoPreserved(patch jsondiff.Patch) jsondiff.Patch {
	prefixes := make([]string, 0, len(autoPreservedFields)*2)
	for _, f := range autoPreservedFields {
		prefixes = append(prefixes, "/"+f) // exact match
	}
	out := make(jsondiff.Patch, 0, len(patch))
	for _, op := range patch {
		if isAutoPreservedPath(op.Path, prefixes) {
			continue
		}
		out = append(out, op)
	}
	return out
}

// isAutoPreservedPath reports whether a JSON Pointer path is exactly
// one of the auto-preserved root paths OR a descendant of one (e.g.
// `/trackerDomains/0`, `/freeSpaceSource/path`). Centralised so the
// match rule stays consistent if we ever extend it.
func isAutoPreservedPath(path string, rootPaths []string) bool {
	for _, root := range rootPaths {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// rewriteAppendsToTrailingMarker converts `add /path/N` (positional
// append at the current end of the array) into `add /path/-`
// (idempotent append-to-end marker). This is the SINGLE most
// important robustness transform per the spec: a `-` append survives
// any number of upstream insertions/removals inside the same array,
// while a positional append breaks the moment upstream adds or removes
// any sibling element.
//
// Only rewrites when the numeric index equals the current length of
// the array AT THE GIVEN PATH IN UPSTREAM — otherwise the op isn't
// actually appending, it's inserting at a specific index, and `-`
// would change the semantics.
//
// Allocates a fresh slice — never mutates the caller's input. The
// previous in-place form was harmless today (last transform in the
// pipeline) but would surface as a bug if transform ordering ever
// changed. See review finding C5.
func rewriteAppendsToTrailingMarker(patch jsondiff.Patch, upstream []byte) jsondiff.Patch {
	out := make(jsondiff.Patch, len(patch))
	copy(out, patch)
	for i, op := range out {
		if op.Type != "add" {
			continue
		}
		idx := strings.LastIndex(op.Path, "/")
		if idx < 0 || idx == len(op.Path)-1 {
			continue
		}
		lastSeg := op.Path[idx+1:]
		n, err := strconv.Atoi(lastSeg)
		if err != nil || n < 0 {
			continue
		}
		parent := op.Path[:idx]
		arrLen, ok := lookupArrayLen(upstream, parent)
		if !ok || arrLen != n {
			continue
		}
		out[i].Path = parent + "/-"
	}
	return out
}

// stripSetEqualArrayReorderings removes `replace` ops on arrays whose
// old and new contents are set-equal — same elements, different
// order. Used to filter noise like `trackerDomains: ["a","b"] →
// ["b","a"]` that has no behavioral effect but produces a replace op.
//
// v1 limitation: only handles single `replace` ops on whole arrays.
// jsondiff sometimes emits per-element add/remove sequences for
// reorderings, which this strip does NOT recognise. Real-world
// trackerDomains reorderings hit the auto-preserved strip first
// (trackerDomains is in DefaultPreservedFields) so the gap rarely
// bites in production. Phase 1.x polish if/when users report
// reordering noise on non-preserved arrays.
//
// Allocates a fresh slice — never mutates the caller's input (review
// finding B2 — same class as stripAutoPreserved).
func stripSetEqualArrayReorderings(patch jsondiff.Patch, upstream, live []byte) jsondiff.Patch {
	if len(patch) == 0 {
		return patch
	}
	out := make(jsondiff.Patch, 0, len(patch))
	for _, op := range patch {
		if op.Type != "replace" {
			out = append(out, op)
			continue
		}
		// Look up the path in BOTH upstream and live; both must be
		// arrays AND set-equal for us to drop the op.
		upArr, upOk := lookupArray(upstream, op.Path)
		liveArr, liveOk := lookupArray(live, op.Path)
		if !upOk || !liveOk {
			out = append(out, op)
			continue
		}
		if !setsEqual(upArr, liveArr) {
			out = append(out, op)
			continue
		}
		// Drop — meaningless reordering.
	}
	return out
}

// ---- helpers ----

// transformedPatchApplies asserts that a (transformed) patch can be
// decoded and applied to upstream without error. This is the weak
// guarantee we can give about the transformed patch — it intentionally
// drops ops touching auto-preserved fields so it CAN'T reproduce live
// exactly, but if it doesn't apply at all, transforms have corrupted
// the patch and we must refuse to persist. Belt-and-braces against
// future transform bugs (review finding B1).
func transformedPatchApplies(upstream, patchJSON []byte) error {
	decoded, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return fmt.Errorf("decode transformed patch: %w", err)
	}
	if _, err := decoded.Apply(upstream); err != nil {
		return fmt.Errorf("apply transformed patch to upstream: %w", err)
	}
	return nil
}

// roundtripValidate applies the raw patch to upstream and compares
// the result to live. Returns an error if they differ — defensive
// against jsondiff bugs or whitespace canonicalisation surprises.
func roundtripValidate(upstream, live []byte, patch jsondiff.Patch) error {
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal raw patch: %w", err)
	}
	decoded, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return fmt.Errorf("decode raw patch: %w", err)
	}
	applied, err := decoded.Apply(upstream)
	if err != nil {
		return fmt.Errorf("apply raw patch to upstream: %w", err)
	}
	// Canonicalise both via re-marshal + sha compare to ignore
	// whitespace/key-order differences.
	if sha256Hex(applied) != sha256Hex(live) {
		// Try a softer canonical compare via re-marshal of parsed
		// objects (handles key-order differences which sha-of-bytes
		// would miss).
		canonApplied, errA := canonicalize(applied)
		canonLive, errL := canonicalize(live)
		if errA == nil && errL == nil && sha256Hex(canonApplied) == sha256Hex(canonLive) {
			return nil
		}
		return fmt.Errorf("patch applied to upstream does not equal live")
	}
	return nil
}

// canonicalize re-marshals JSON via Go's encoding/json so keys land
// in sorted order — defangs spurious roundtrip mismatches caused by
// key-order differences that don't matter semantically.
func canonicalize(data []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// stripFields returns a copy of the JSON document with the given
// top-level fields removed. Used to normalise upstream + live before
// diff computation so server-assigned metadata + maintainer-only
// fields don't show up as spurious diff ops. Only top-level fields
// are removed — nested occurrences (deliberately rare for the fields
// we strip) survive.
func stripFields(data []byte, fields []string) ([]byte, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	for _, f := range fields {
		delete(doc, f)
	}
	return json.Marshal(doc)
}

// extractSchemaVersion pulls conditions.schemaVersion out of a rule
// payload. Returns "1" when missing — Qui's current schema, which is
// the safe fallback when the field is absent.
//
// Uses json.RawMessage to stringify whatever Qui sends — defensive
// against schema-version bumps where Qui might emit "2" as a number
// instead of a string. Previously typed as `string`, which would
// silently swallow a numeric value and report "1" — masking a real
// schema bump (review finding B3+C2). Now: numeric, boolean, and
// object values all produce a non-"1" sentinel that flows through to
// Apply's schemaVersion comparison and triggers a conflict on drift.
func extractSchemaVersion(upstream []byte) string {
	var probe struct {
		Conditions struct {
			SchemaVersion json.RawMessage `json:"schemaVersion"`
		} `json:"conditions"`
	}
	if err := json.Unmarshal(upstream, &probe); err != nil {
		return "1"
	}
	raw := strings.TrimSpace(string(probe.Conditions.SchemaVersion))
	if raw == "" || raw == "null" {
		return "1"
	}
	// String form ("1") — trim the JSON quoting so callers see the
	// raw value, matching pre-fix behaviour for the common case.
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal(probe.Conditions.SchemaVersion, &s); err == nil {
			if s == "" {
				return "1"
			}
			return s
		}
	}
	// Non-string form (number, bool, object) — return the canonical
	// JSON literal as-is so any future-Qui drift surfaces as a
	// schema-bump conflict at Apply time, not as a silent equality.
	return raw
}

// sha256Hex hashes arbitrary bytes to a lowercase hex string.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// isEmptyDiff reports whether a serialized patch represents zero ops.
// Empty patches serialise as `[]`; some libraries also produce `null`
// for the empty case — accept both.
func isEmptyDiff(patchJSON []byte) bool {
	s := strings.TrimSpace(string(patchJSON))
	return s == "[]" || s == "null" || s == ""
}

// lookupArrayLen navigates a JSON document by JSON Pointer path and
// returns the length of the array at that path. Returns ok=false when
// the path doesn't exist or doesn't point at an array.
func lookupArrayLen(doc []byte, path string) (int, bool) {
	arr, ok := lookupArray(doc, path)
	if !ok {
		return 0, false
	}
	return len(arr), true
}

// lookupArray navigates a JSON document by JSON Pointer path and
// returns the raw elements of the array there.
func lookupArray(doc []byte, path string) ([]json.RawMessage, bool) {
	if path == "" {
		// Root path — top-level array case.
		var arr []json.RawMessage
		if err := json.Unmarshal(doc, &arr); err != nil {
			return nil, false
		}
		return arr, true
	}
	// Walk the path segment-by-segment using a generic interface.
	var current interface{}
	if err := json.Unmarshal(doc, &current); err != nil {
		return nil, false
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, seg := range segs {
		// JSON Pointer escape per RFC 6901 §4: unescape ~1 → / FIRST,
		// then ~0 → ~. Order matters: a literal segment of `~01` must
		// resolve to `~1` (NOT to `~/`). Do not "simplify" by combining
		// or reversing these two ReplaceAll calls.
		seg = strings.ReplaceAll(seg, "~1", "/")
		seg = strings.ReplaceAll(seg, "~0", "~")
		switch v := current.(type) {
		case map[string]interface{}:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			current = next
		case []interface{}:
			n, err := strconv.Atoi(seg)
			if err != nil || n < 0 || n >= len(v) {
				return nil, false
			}
			current = v[n]
		default:
			return nil, false
		}
	}
	arr, ok := current.([]interface{})
	if !ok {
		return nil, false
	}
	// Re-encode each element so callers can compare as canonical JSON.
	out := make([]json.RawMessage, 0, len(arr))
	for _, el := range arr {
		raw, err := json.Marshal(el)
		if err != nil {
			return nil, false
		}
		out = append(out, raw)
	}
	return out, true
}

// setsEqual reports whether two slices of JSON values are set-equal —
// same multiset of canonical-JSON strings, ignoring order. Defangs
// trackerDomains-style reorderings without flagging structurally-
// different arrays.
func setsEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	as := make([]string, len(a))
	bs := make([]string, len(b))
	for i, raw := range a {
		c, err := canonicalize(raw)
		if err != nil {
			return false
		}
		as[i] = string(c)
	}
	for i, raw := range b {
		c, err := canonicalize(raw)
		if err != nil {
			return false
		}
		bs[i] = string(c)
	}
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
