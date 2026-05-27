package customize

import (
	"encoding/json"
	"testing"
)

// Integration tests R1-R4 from design §9.3. These exercise SemanticDiff
// against realistic Qui-rule shapes. R5/R6 are apply-side; tested in
// semantic_apply_test.go (Phase D).

// R1 — tester's reproducer. User inserts TAGS NOT_EQUAL upload at the
// START of conditions. With v0.5 (JSON Patch) this exploded into 12
// replace ops. SemanticDiff must produce exactly 1 add op with
// Position=start, no replaces.
func TestSemanticDiff_R1_AddAtStart_TesterCase(t *testing.T) {
	upstream := jsonObj(t, `{
		"name": "Delete: noHL Tier 1",
		"conditions": {
			"delete": {
				"condition": {
					"operator": "AND",
					"conditions": [
						{"operator":"OR","conditions":[
							{"field":"TAGS","operator":"EQUAL","value":"Tier1"}]},
						{"field":"NAME","operator":"MATCHES","value":"S\\d+"}]
				}
			}
		}
	}`)
	live := jsonObj(t, `{
		"name": "Delete: noHL Tier 1",
		"conditions": {
			"delete": {
				"condition": {
					"operator": "AND",
					"conditions": [
						{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
						{"operator":"OR","conditions":[
							{"field":"TAGS","operator":"EQUAL","value":"Tier1"}]},
						{"field":"NAME","operator":"MATCHES","value":"S\\d+"}]
				}
			}
		}
	}`)

	ops := SemanticDiff(upstream, live)
	if len(ops) != 1 {
		t.Fatalf("R1: expected 1 op (add at start), got %d:\n%s", len(ops), formatOps(ops))
	}
	op := ops[0]
	if op.Kind != OpAdd {
		t.Fatalf("R1: expected OpAdd, got %s", op.Kind)
	}
	if op.Position == nil || op.Position.Kind != "start" {
		t.Fatalf("R1: expected Position.Kind='start', got %+v", op.Position)
	}
	if !op.Position.FallbackEnd {
		t.Fatalf("R1: expected FallbackEnd=true at capture time")
	}
	// Path should reach the innermost conditions array.
	wantPath := []string{"conditions", "delete", "condition", "conditions"}
	if !equalStringSlice(op.Path, wantPath) {
		t.Fatalf("R1: path mismatch — got %v want %v", op.Path, wantPath)
	}
	// Value should be the new leaf.
	if !matchesLeaf(op.Value, "TAGS", "NOT_EQUAL", "upload") {
		t.Fatalf("R1: added value is not the TAGS-upload leaf: %+v", op.Value)
	}
}

// R2 — user modified one leaf's value. Maintainer modified another
// unrelated leaf separately (we test the capture side here — apply-side
// "both survive" is in Phase D). SemanticDiff against current upstream
// must produce exactly 1 Replace op (user's), with the OLD upstream fp.
func TestSemanticDiff_R2_ValueModification(t *testing.T) {
	// At capture time, upstream still has the original leaf values.
	// User has changed COMPLETION_ON from 1836000 → 1843200 in their Qui.
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"},
			{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"},
			{"field":"NUM_COMPLETE","operator":"GREATER_THAN_OR_EQUAL","value":"3"}]
	}`)

	ops := SemanticDiff(upstream, live)
	if len(ops) != 1 {
		t.Fatalf("R2: expected exactly 1 Replace op, got %d:\n%s", len(ops), formatOps(ops))
	}
	op := ops[0]
	if op.Kind != OpReplace {
		t.Fatalf("R2: expected OpReplace, got %s", op.Kind)
	}
	if op.Field != "value" {
		t.Fatalf("R2: expected Field='value', got %q", op.Field)
	}
	if op.Value != "1843200" {
		t.Fatalf("R2: expected Value='1843200', got %v", op.Value)
	}
	// Match.Fingerprint must be the OLD (upstream-side) fp — that's the
	// implicit compare-and-swap for value_drifted detection in apply.
	upLeaf := jsonObj(t, `{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}`)
	wantFp := Compute(upLeaf)
	if op.Match == nil || op.Match.Fingerprint != wantFp {
		got := ""
		if op.Match != nil {
			got = string(op.Match.Fingerprint)
		}
		t.Fatalf("R2: expected Match.Fingerprint=%s (old upstream fp), got %s", wantFp, got)
	}
}

// R3 — user removed a leaf from a conditions list. SemanticDiff must
// emit a Remove op with Match.Fingerprint = the removed element's fp.
// (Apply-side conflict-on-rematch is Phase D.)
func TestSemanticDiff_R3_UserRemovesLeaf(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"}]
	}`)
	// User removed the B leaf.
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)

	ops := SemanticDiff(upstream, live)
	if len(ops) != 1 {
		t.Fatalf("R3: expected 1 Remove, got %d:\n%s", len(ops), formatOps(ops))
	}
	op := ops[0]
	if op.Kind != OpRemove {
		t.Fatalf("R3: expected OpRemove, got %s", op.Kind)
	}
	removedLeaf := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"B"}`)
	wantFp := Compute(removedLeaf)
	if op.Match == nil || op.Match.Fingerprint != wantFp {
		t.Fatalf("R3: Remove must reference the removed leaf's fp (%s), got %+v", wantFp, op.Match)
	}
}

// R4 — Maintainer reordered the array (3 leaves shuffled). User has
// no customize change. SemanticDiff(upstream-old, live-which-equals-old)
// produces zero ops — that's the baseline.
//
// The real R4 in design §9.3 talks about apply-time stability against
// upstream reorder. We test the CAPTURE-side here: maintainer-shuffle
// with NO user change → 0 ops, because identity pairs all match across
// reorder (group fingerprint is stable under child reorder, but for
// arrays of LEAVES, identity match groups them by fp regardless of
// position).
func TestSemanticDiff_R4_MaintainerReorder_NoUserChange(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"},
			{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)
	// Live = upstream byte-equal (user did nothing).
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"},
			{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)

	ops := SemanticDiff(upstream, live)
	if len(ops) != 0 {
		t.Fatalf("R4 baseline: identical inputs must produce 0 ops, got %d:\n%s", len(ops), formatOps(ops))
	}

	// Now the more interesting variant: maintainer reorders, user has
	// one replace, capture sees user's replace only — the reorder
	// itself is invisible because identity pairs match by fp.
	upstreamReordered := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"C"},
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"}]
	}`)
	liveOfReorderWithUserChange := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"C"},
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B-modified"}]
	}`)
	ops2 := SemanticDiff(upstreamReordered, liveOfReorderWithUserChange)
	if len(ops2) != 1 {
		t.Fatalf("R4 with user-modification: expected exactly 1 Replace, got %d:\n%s", len(ops2), formatOps(ops2))
	}
	if ops2[0].Kind != OpReplace || ops2[0].Field != "value" {
		t.Fatalf("R4: expected Replace on field=value, got Kind=%s Field=%q", ops2[0].Kind, ops2[0].Field)
	}
}

// ---- supporting coverage for the diff infrastructure ----

func TestSemanticDiff_GroupRecursion_EmitsDescend(t *testing.T) {
	// Upstream has a group whose child has value X. Live has same
	// group structure but child has value Y. Group fingerprint differs
	// (recursive hashing), so it's NOT in IdentityPairs — it's a
	// remove+add at the OUTER level. That's the structural-change
	// case, not a Descend.
	//
	// To get a Descend op, we need the outer match (Pass 1) to succeed
	// but the inner contents to differ. That happens when the group's
	// fingerprint is stable BUT diffNode finds inner differences.
	// However, since group fingerprint includes all child fps, this is
	// a contradiction — the algorithm is consistent: Descend only
	// fires for groups whose outer fp matches but recursive diff still
	// finds inner diffs. In practice this would only happen for nested
	// object/scalar-key changes inside a group (not array-item changes
	// which already break the group fp).
	//
	// So this test exercises the harder case: a group with a sibling
	// scalar key changing inside the same nested-object container.

	upstream := jsonObj(t, `{
		"conditions": [
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"X"}],
			 "enabled": true}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"operator":"AND","conditions":[
				{"field":"TAGS","operator":"EQUAL","value":"X"}],
			 "enabled": false}]
	}`)
	// Group fp includes "operator" and child-fps, NOT "enabled" — so
	// the outer group matches identically. The inner "enabled" scalar
	// change should surface via a Descend with an inner Replace op.
	ops := SemanticDiff(upstream, live)
	if len(ops) != 1 {
		t.Fatalf("expected 1 Descend op, got %d:\n%s", len(ops), formatOps(ops))
	}
	if ops[0].Kind != OpDescend {
		t.Fatalf("expected OpDescend, got %s", ops[0].Kind)
	}
	if len(ops[0].SubOps) != 1 {
		t.Fatalf("expected 1 sub-op (Replace on 'enabled'), got %d", len(ops[0].SubOps))
	}
	if ops[0].SubOps[0].Kind != OpReplace || ops[0].SubOps[0].Field != "enabled" {
		t.Fatalf("expected sub-op Replace on 'enabled', got Kind=%s Field=%q",
			ops[0].SubOps[0].Kind, ops[0].SubOps[0].Field)
	}
}

func TestSemanticDiff_ObjectKeyAddRemove(t *testing.T) {
	upstream := jsonObj(t, `{"a": 1, "b": 2}`)
	live := jsonObj(t, `{"a": 1, "c": 3}`)

	ops := SemanticDiff(upstream, live)
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops (add c, remove b), got %d", len(ops))
	}
	// Order is deterministic: keyUnion is sorted, so "b" (remove) emits
	// before "c" (add).
	if ops[0].Kind != OpRemove || ops[1].Kind != OpAdd {
		t.Fatalf("expected Remove then Add, got %s then %s", ops[0].Kind, ops[1].Kind)
	}
}

func TestSemanticDiff_NoChanges(t *testing.T) {
	rule := jsonObj(t, `{
		"name": "noop",
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"X"}]
	}`)
	ops := SemanticDiff(rule, rule)
	if len(ops) != 0 {
		t.Fatalf("identical inputs must produce 0 ops, got %d", len(ops))
	}
}

func TestSemanticDiff_AddInMiddle_PositionAnchor(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"},
			{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)
	ops := SemanticDiff(upstream, live)
	if len(ops) != 1 {
		t.Fatalf("expected 1 Add, got %d:\n%s", len(ops), formatOps(ops))
	}
	op := ops[0]
	if op.Kind != OpAdd {
		t.Fatalf("expected OpAdd, got %s", op.Kind)
	}
	// Insert is between A (matched) and C (matched), lvIdx=1 (not last).
	// derivePosition walks backward from lvIdx-1=0, finds matched A
	// → emits Position{Kind: "after", Anchor: fp(A)}.
	if op.Position == nil || op.Position.Kind != "after" {
		t.Fatalf("expected Position.Kind='after', got %+v", op.Position)
	}
	wantAnchor := Compute(jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"A"}`))
	if op.Position.Anchor != wantAnchor {
		t.Fatalf("expected Anchor=fp(A)=%s, got %s", wantAnchor, op.Position.Anchor)
	}
}

// ---- helpers ----

func matchesLeaf(v any, field, op, value string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	f, _ := m["field"].(string)
	o, _ := m["operator"].(string)
	vstr, _ := m["value"].(string)
	return f == field && o == op && vstr == value
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatOps(ops []Op) string {
	b, _ := json.MarshalIndent(ops, "  ", "  ")
	return string(b)
}
