// apply_bridge.go — []byte ↔ any conversion shim between the v2
// semantic engine and the rest of the sync code, which still passes
// JSON-as-[]byte across module boundaries.
//
// Why this exists: SemanticApply / SemanticDiff work on
// JSON-decoded trees (`any`, ultimately `map[string]any` /
// `[]any` / scalars). The sync engine in `internal/core/sync.go`
// holds upstream rule bodies as raw []byte (read from disk or
// HTTP). Rather than push the any-tree representation up through
// every callsite — which would touch buildUpdatePayload, the
// Layer-3 preserve pass, and the QuiClient PUT body — we wrap
// the conversion in this one place and keep callsites looking
// like the v0.5 Apply they replaced.
//
// Phase E.2 — Caller rewrite. Used by sync.go's apply path and
// by customize_handlers.go's diff-preview endpoint.

package customize

import (
	"encoding/json"
	"fmt"
)

// ApplyBridge takes a JSON-encoded upstream rule + a stored v2
// customization, runs SemanticApply, and returns (effectiveRule,
// conflicts, err). The error channel is reserved for actual JSON
// parse/serialize failures — apply-time conflicts are surfaced via
// the conflicts slice without an accompanying error.
//
//   effective == nil  AND  err == nil  → caller treats as "no
//   change vs upstream" (the conflicts slice will tell why if it
//   isn't empty; typically all-conflicts means every op was
//   skipped and result == upstream).
//
//   effective != nil  AND  conflicts == nil → clean apply, PUT
//   to Qui.
//
//   effective != nil  AND  conflicts != nil → apply produced an
//   effective tree BUT some ops were skipped; UI should still
//   surface the conflicts list. The caller's policy decides
//   whether to PUT the effective result or block on review.
func ApplyBridge(upstream []byte, c *CustomizationV2) ([]byte, []OpConflict, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("customization is nil")
	}
	if len(upstream) == 0 {
		return nil, nil, fmt.Errorf("upstream is empty")
	}

	var upObj any
	if err := json.Unmarshal(upstream, &upObj); err != nil {
		return nil, nil, fmt.Errorf("decode upstream: %w", err)
	}

	result, conflicts := SemanticApply(upObj, c.Ops)

	out, err := json.Marshal(result)
	if err != nil {
		return nil, conflicts, fmt.Errorf("encode result: %w", err)
	}
	return out, conflicts, nil
}

// noiseFieldsV2 are framework artifacts present on one side of an
// upstream/live pair but never user-meaningful. Stripped from BOTH
// sides before SemanticDiff so they don't flood the op list with
// server-assigned metadata mismatches (id, createdAt etc. appear on
// live from the Qui API but not on upstream, which is the file on
// disk; _slug + _description go the other way). Mirrors v0.5
// capture.go::noiseFields — keep in sync if either expands.
var noiseFieldsV2 = []string{
	"id",
	"instanceId",
	"_slug",
	"_description",
	"createdAt",
	"updatedAt",
}

// autoPreservedFieldsV2 are user-owned top-level scalars that Layer 3
// of sync.go's apply pipeline ALWAYS merges from live Qui state
// (trackers, intervals, on/off, dry-run, notify). Customize must NOT
// emit ops against these paths: doing so would let a captured diff
// overrule the preserve mechanism, breaking the spec's "auto-fields
// always win" invariant. Mirrors v0.5 capture.go::autoPreservedFields
// — and ultimately core.DefaultPreservedFields() (asserted in a
// consistency test in the core package).
var autoPreservedFieldsV2 = []string{
	"trackerPattern",
	"trackerDomains",
	"intervalSeconds",
	"freeSpaceSource",
	"enabled",
	"dryRun",
	"notify",
}

// DiffBridge is the capture-side companion: takes JSON-encoded
// upstream + live, normalises both (strip noise + auto-preserved
// fields, per v0.5 parity), runs SemanticDiff, returns the op list
// (or nil if there are no changes). Used by handleCaptureCustomization
// + handleSetupDiff; both want the op list before persisting.
//
// The strip step is critical: without it, server-assigned metadata
// like `id`, `createdAt`, `instanceId` show up as huge runs of
// add/remove ops (one side has them, the other doesn't), and
// user-owned auto-preserved fields like `trackerPattern` would be
// captured into customize state — then re-applied at sync time even
// though Layer 3 separately preserves them from live. Result without
// stripping: ops list either spuriously huge OR spuriously empty
// depending on whether the noise differs on the specific (upstream,
// live) pair.
func DiffBridge(upstream, live []byte) ([]Op, error) {
	if len(upstream) == 0 || len(live) == 0 {
		return nil, fmt.Errorf("upstream and live are required")
	}

	var upObj, lvObj any
	if err := json.Unmarshal(upstream, &upObj); err != nil {
		return nil, fmt.Errorf("decode upstream: %w", err)
	}
	if err := json.Unmarshal(live, &lvObj); err != nil {
		return nil, fmt.Errorf("decode live: %w", err)
	}

	stripTopLevelFields(upObj, noiseFieldsV2)
	stripTopLevelFields(lvObj, noiseFieldsV2)
	stripTopLevelFields(upObj, autoPreservedFieldsV2)
	stripTopLevelFields(lvObj, autoPreservedFieldsV2)

	ops := SemanticDiff(upObj, lvObj)
	if len(ops) == 0 {
		return nil, nil // semantically: "no changes" — caller decides UX
	}
	return ops, nil
}

// stripTopLevelFields removes keys from a decoded JSON object root.
// No-op when the value isn't an object (defensive — diff inputs are
// always objects today but the helper stays safe under shape drift).
func stripTopLevelFields(t any, keys []string) {
	m, ok := t.(map[string]any)
	if !ok {
		return
	}
	for _, k := range keys {
		delete(m, k)
	}
}
