// nested_group_test.go — regression coverage for the tester-reported
// data-loss bug on rules with NESTED condition groups.
//
// Real-world Qui rules wrap their conditions in a tree:
//
//	condition (OR)
//	  └─ conditions: [ AND-group-1, AND-group-2, ... ]
//	        └─ each AND-group has conditions: [ leaf, leaf, ... ]
//
// When the user adds/edits/removes a LEAF inside one of those nested
// groups, the group's full fingerprint changes (groupFingerprint hashes
// its children). The original two-pass matcher had no way to pair a
// group whose interior changed — Pass 1 (full fp) missed it and Pass 2
// is leaf-only — so the whole group surfaced as remove+add. That made
// the customization swallow the entire block, and on the next sync the
// user's stale copy of the block overwrote every maintainer edit inside
// it (e.g. a changed torrent-age threshold silently reverted).
//
// These tests pin the granular behaviour: a single nested leaf edit must
// produce a Descend chain ending in ONE op, and applying that op onto a
// maintainer-edited upstream must keep BOTH the user's edit and the
// maintainer's unrelated value change.

package customize

import (
	"encoding/json"
	"testing"
)

// baseNested is the maintainer's rule at capture time: one OR-wrapper
// holding two AND-groups. COMPLETION_ON (torrent age) is 1836000 (510h).
const baseNested = `{
	"conditions": {"delete": {"condition": {"operator":"OR","conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"Tier1"},
			{"field":"TAGS","operator":"EQUAL","value":"noHL"},
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"},
			{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}
		]},
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"noHL"},
			{"field":"TRACKER","operator":"EQUAL","value":"HDB"}
		]}
	]}}},
	"sortOrder": 15
}`

// liveNested is the user's copy: identical to baseNested except a single
// "TAGS NOT_EQUAL upload" leaf prepended to the FIRST AND-group, plus a
// sortOrder bump. This is exactly the tester's "upload rule" customization.
const liveNested = `{
	"conditions": {"delete": {"condition": {"operator":"OR","conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
			{"field":"TAGS","operator":"EQUAL","value":"Tier1"},
			{"field":"TAGS","operator":"EQUAL","value":"noHL"},
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"},
			{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}
		]},
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"noHL"},
			{"field":"TRACKER","operator":"EQUAL","value":"HDB"}
		]}
	]}}},
	"sortOrder": 18
}`

func TestSemanticDiff_NestedGroup_AddLeafIsGranular(t *testing.T) {
	base := jsonObj(t, baseNested)
	live := jsonObj(t, liveNested)

	ops := SemanticDiff(base, live)

	// The bug signature: a remove and/or add op sitting directly on the
	// nested condition array, carrying the WHOLE OR-wrapper as its value.
	nestedArrPath := []string{"conditions", "delete", "condition", "conditions"}
	for _, op := range ops {
		if (op.Kind == OpRemove || op.Kind == OpAdd) && equalStringSlice(op.Path, nestedArrPath) {
			t.Fatalf("regression: wholesale %s on nested condition array (expected granular Descend):\n%s",
				op.Kind, formatOps(ops))
		}
	}

	// Round-trip must reconstruct live exactly, with no conflicts.
	got, conflicts := SemanticApply(base, ops)
	if len(conflicts) != 0 {
		t.Fatalf("round-trip produced conflicts: %+v", conflicts)
	}
	if !deepEqual(got, live) {
		t.Fatalf("round-trip mismatch:\nops:\n%s", formatOps(ops))
	}
}

// This is the tester's actual expectation, stated verbatim: "with
// everything matching the repo besides the upload rule at the time of
// the detection it should have applied the new torrent age."
//
//	BASE   = upstream at capture (torrent age 1836000)
//	MINE   = live at capture     (BASE + upload rule, torrent age 1836000)
//	THEIRS = upstream at sync     (BASE but torrent age bumped to 2000000)
//
// Applying diff(BASE, MINE) onto THEIRS must yield: the upload rule
// present AND the maintainer's new torrent age (2000000) preserved.
func TestSemanticApply_NestedGroup_MaintainerValueEditPreserved(t *testing.T) {
	base := jsonObj(t, baseNested)
	mine := jsonObj(t, liveNested)
	theirs := jsonObj(t, `{
		"conditions": {"delete": {"condition": {"operator":"OR","conditions":[
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"Tier1"},
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"2000000"},
				{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}
			]},
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"TRACKER","operator":"EQUAL","value":"HDB"}
			]}
		]}}},
		"sortOrder": 15
	}`)

	ops := SemanticDiff(base, mine)
	got, conflicts := SemanticApply(theirs, ops)

	for _, c := range conflicts {
		if c.Severity == SeverityHigh || c.Severity == SeverityMedium {
			t.Fatalf("unexpected blocking conflict: %+v\nops:\n%s", c, formatOps(ops))
		}
	}

	g1 := firstANDGroup(t, got)
	if !hasLeaf(g1, "TAGS", "NOT_EQUAL", "upload") {
		t.Fatalf("user's upload rule was not applied:\n%s", formatOps(ops))
	}
	if !hasLeaf(g1, "COMPLETION_ON", "GREATER_THAN", "2000000") {
		t.Fatalf("maintainer's new torrent age (2000000) was not preserved:\n%s", formatOps(ops))
	}
	if hasLeaf(g1, "COMPLETION_ON", "GREATER_THAN", "1836000") {
		t.Fatalf("stale torrent age (1836000) leaked back in — user copy overwrote maintainer edit")
	}
}

// Two sibling groups share the SAME skeleton (same fields+operators,
// different values). The user customised one of them; the maintainer then
// edited a value inside that same group (shifting its full fingerprint).
// Apply must descend into the value-edited ORIGINAL, not its look-alike —
// even when the look-alike sits FIRST in the array (so a naive
// first-skeleton-match would pick wrong). Proves children-overlap, not
// position, drives the choice.
func TestSemanticApply_SameSkeletonSiblings_PicksByOverlap(t *testing.T) {
	// Look-alike (B) first, customised group (A) second.
	base := jsonObj(t, `{"conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"b1"},
			{"field":"TRACKER","operator":"EQUAL","value":"t2"}
		]},
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"a1"},
			{"field":"TRACKER","operator":"EQUAL","value":"t1"}
		]}
	]}`)
	// User adds FLAG to group A (the second one).
	mine := jsonObj(t, `{"conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"b1"},
			{"field":"TRACKER","operator":"EQUAL","value":"t2"}
		]},
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"a1"},
			{"field":"TRACKER","operator":"EQUAL","value":"t1"},
			{"field":"FLAG","operator":"EQUAL","value":"x"}
		]}
	]}`)
	// Maintainer edits group A's TAGS value (a1 -> a2); full fp shifts.
	theirs := jsonObj(t, `{"conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"b1"},
			{"field":"TRACKER","operator":"EQUAL","value":"t2"}
		]},
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"a2"},
			{"field":"TRACKER","operator":"EQUAL","value":"t1"}
		]}
	]}`)

	ops := SemanticDiff(base, mine)
	got, conflicts := SemanticApply(theirs, ops)
	for _, c := range conflicts {
		if c.Severity == SeverityHigh || c.Severity == SeverityMedium {
			t.Fatalf("unexpected blocking conflict: %+v", c)
		}
	}

	conds := topConditions(t, got)
	if len(conds) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(conds))
	}
	groupB := groupLeaves(t, conds[0])
	groupA := groupLeaves(t, conds[1])
	if hasLeaf(groupB, "FLAG", "EQUAL", "x") {
		t.Fatalf("FLAG leaked into the look-alike group B")
	}
	if !hasLeaf(groupA, "FLAG", "EQUAL", "x") {
		t.Fatalf("FLAG was not applied to the customised group A")
	}
	if !hasLeaf(groupA, "TAGS", "EQUAL", "a2") {
		t.Fatalf("maintainer's TAGS edit (a2) was not preserved in group A")
	}
}

// When the exact customised group is gone and two equally-plausible
// look-alikes survive (each sharing the same number of children), apply
// must NOT guess — it blocks with a medium conflict and applies nothing.
func TestSemanticApply_AmbiguousSkeleton_Blocks(t *testing.T) {
	base := jsonObj(t, `{"conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"a"},
			{"field":"TRACKER","operator":"EQUAL","value":"t"}
		]}
	]}`)
	mine := jsonObj(t, `{"conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"a"},
			{"field":"TRACKER","operator":"EQUAL","value":"t"},
			{"field":"FLAG","operator":"EQUAL","value":"x"}
		]}
	]}`)
	// Exact group gone; two twins each share exactly one captured child.
	theirs := jsonObj(t, `{"conditions":[
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"a"},
			{"field":"TRACKER","operator":"EQUAL","value":"u"}
		]},
		{"operator":"AND","conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"c"},
			{"field":"TRACKER","operator":"EQUAL","value":"t"}
		]}
	]}`)

	ops := SemanticDiff(base, mine)
	got, conflicts := SemanticApply(theirs, ops)

	blocked := false
	for _, c := range conflicts {
		if c.Reason == ConflictAmbiguous && c.Severity == SeverityMedium {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("expected a medium ambiguous conflict, got %+v", conflicts)
	}
	for _, g := range topConditions(t, got) {
		if hasLeaf(groupLeaves(t, g), "FLAG", "EQUAL", "x") {
			t.Fatalf("FLAG was applied despite ambiguity — should have blocked")
		}
	}
}

// End-to-end through the SAME functions the HTTP handlers call:
// DiffBridge (capture / Detect changes) → CustomizationV2 → ApplyBridge
// (sync). Uses realistic full Qui rules carrying server-assigned noise
// (id, _slug, createdAt) and an auto-preserved field (enabled) so the
// strip step is exercised too. This is the closest in-process mirror of
// the tester's click-through.
func TestBridge_NestedGroup_TesterScenario_EndToEnd(t *testing.T) {
	// Upstream rule on disk (maintainer repo): torrent age 510h, no _id.
	upstream := []byte(`{
		"_slug": "delete-noHL",
		"_description": "noHL delete rule",
		"enabled": true,
		"sortOrder": 15,
		"conditions": {"delete": {"condition": {"operator":"OR","conditions":[
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"Tier1"},
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"},
				{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}
			]},
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"TRACKER","operator":"EQUAL","value":"HDB"}
			]}
		]}}}
	}`)

	// Live rule from the Qui API: same body + the user's upload rule +
	// server-assigned id/createdAt + a flipped auto-preserved field.
	live := []byte(`{
		"id": 42,
		"instanceId": 7,
		"createdAt": "2026-05-01T00:00:00Z",
		"enabled": false,
		"sortOrder": 18,
		"conditions": {"delete": {"condition": {"operator":"OR","conditions":[
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
				{"field":"TAGS","operator":"EQUAL","value":"Tier1"},
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"},
				{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}
			]},
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"TRACKER","operator":"EQUAL","value":"HDB"}
			]}
		]}}}
	}`)

	// --- Capture (Detect changes) ---
	ops, err := DiffBridge(upstream, live)
	if err != nil {
		t.Fatalf("DiffBridge: %v", err)
	}
	if len(ops) == 0 {
		t.Fatalf("DiffBridge found no changes — should capture the upload rule")
	}
	// Must be granular: no op may add/remove the whole nested condition array.
	nestedArrPath := []string{"conditions", "delete", "condition", "conditions"}
	for _, op := range ops {
		if (op.Kind == OpRemove || op.Kind == OpAdd) && equalStringSlice(op.Path, nestedArrPath) {
			t.Fatalf("regression: wholesale %s of nested condition array:\n%s", op.Kind, formatOps(ops))
		}
	}
	// enabled (auto-preserved) and sortOrder differ between sides; enabled
	// must NOT be captured (Layer 3 owns it), sortOrder should be.
	for _, op := range ops {
		if op.Field == "enabled" {
			t.Fatalf("auto-preserved field 'enabled' must not be captured:\n%s", formatOps(ops))
		}
	}

	// --- Sync onto a maintainer-updated upstream (torrent age 510h->555h) ---
	theirs := []byte(`{
		"_slug": "delete-noHL",
		"_description": "noHL delete rule (tightened)",
		"enabled": true,
		"sortOrder": 15,
		"conditions": {"delete": {"condition": {"operator":"OR","conditions":[
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"Tier1"},
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1998000"},
				{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}
			]},
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"noHL"},
				{"field":"TRACKER","operator":"EQUAL","value":"HDB"}
			]}
		]}}}
	}`)

	c := &CustomizationV2{SchemaVersion: CurrentSchemaVersion, Ops: ops}
	effBytes, conflicts, err := ApplyBridge(theirs, c)
	if err != nil {
		t.Fatalf("ApplyBridge: %v", err)
	}
	for _, cf := range conflicts {
		if cf.Severity == SeverityHigh || cf.Severity == SeverityMedium {
			t.Fatalf("unexpected blocking conflict: %+v\nops:\n%s", cf, formatOps(ops))
		}
	}

	var eff any
	if err := json.Unmarshal(effBytes, &eff); err != nil {
		t.Fatalf("decode effective: %v", err)
	}
	g1 := firstANDGroup(t, eff)
	if !hasLeaf(g1, "TAGS", "NOT_EQUAL", "upload") {
		t.Fatalf("user's upload rule not applied after sync:\n%s", formatOps(ops))
	}
	if !hasLeaf(g1, "COMPLETION_ON", "GREATER_THAN", "1998000") {
		t.Fatalf("maintainer's new torrent age (1998000) not preserved:\n%s", formatOps(ops))
	}
	if hasLeaf(g1, "COMPLETION_ON", "GREATER_THAN", "1836000") {
		t.Fatalf("stale torrent age (1836000) leaked back in")
	}
	// sortOrder is the user's captured 18; the maintainer's 15 is overridden.
	if effMap, ok := eff.(map[string]any); ok {
		if so, _ := effMap["sortOrder"].(float64); so != 18 {
			t.Fatalf("expected user's sortOrder 18, got %v", effMap["sortOrder"])
		}
	}
}

// ---- local helpers ----

func topConditions(t *testing.T, rule any) []any {
	t.Helper()
	m, ok := rule.(map[string]any)
	if !ok {
		t.Fatalf("rule is not an object")
	}
	c, ok := m["conditions"].([]any)
	if !ok {
		t.Fatalf("top-level conditions missing")
	}
	return c
}

func groupLeaves(t *testing.T, group any) []any {
	t.Helper()
	m, ok := group.(map[string]any)
	if !ok {
		t.Fatalf("group is not an object")
	}
	l, ok := m["conditions"].([]any)
	if !ok {
		t.Fatalf("group conditions missing")
	}
	return l
}

// firstANDGroup digs out conditions.delete.condition.conditions[0] —
// the first AND-group — and returns its conditions slice.
func firstANDGroup(t *testing.T, rule any) []any {
	t.Helper()
	m, ok := rule.(map[string]any)
	if !ok {
		t.Fatalf("rule is not an object")
	}
	cur := m
	for _, k := range []string{"conditions", "delete", "condition"} {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("missing %q on the way to the OR-wrapper", k)
		}
		cur = next
	}
	wrapper, ok := cur["conditions"].([]any)
	if !ok || len(wrapper) == 0 {
		t.Fatalf("OR-wrapper conditions missing or empty")
	}
	first, ok := wrapper[0].(map[string]any)
	if !ok {
		t.Fatalf("first OR branch is not an object")
	}
	leaves, ok := first["conditions"].([]any)
	if !ok {
		t.Fatalf("first AND-group conditions missing")
	}
	return leaves
}

func hasLeaf(leaves []any, field, op, value string) bool {
	for _, l := range leaves {
		if matchesLeaf(l, field, op, value) {
			return true
		}
	}
	return false
}
