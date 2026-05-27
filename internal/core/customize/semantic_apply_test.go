package customize

import (
	"encoding/json"
	"testing"
)

// Phase D tests for SemanticApply. The headline property is round-trip:
// apply(upstream, diff(upstream, live)) == live, with zero conflicts.
// On top of that we cover R5 + R6 (apply-time conflict detection).

// ---- Round-trip property ----

func TestSemanticApply_RoundTrip_R1_AddAtStart(t *testing.T) {
	// Tester reproducer: user adds TAGS NOT_EQUAL upload at start.
	// apply(upstream, diff(upstream, live)) must equal live.
	upstream := jsonObj(t, `{
		"name": "Delete: noHL Tier 1",
		"conditions": {"delete": {"condition": {"operator": "AND", "conditions": [
			{"operator":"OR","conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier1"}]},
			{"field":"NAME","operator":"MATCHES","value":"S\\d+"}]}}}
	}`)
	live := jsonObj(t, `{
		"name": "Delete: noHL Tier 1",
		"conditions": {"delete": {"condition": {"operator": "AND", "conditions": [
			{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
			{"operator":"OR","conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier1"}]},
			{"field":"NAME","operator":"MATCHES","value":"S\\d+"}]}}}
	}`)
	ops := SemanticDiff(upstream, live)
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts on round-trip, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	assertDeepEqual(t, "round-trip", result, live)
}

func TestSemanticApply_RoundTrip_R2_ValueModification(t *testing.T) {
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
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d", len(conflicts))
	}
	assertDeepEqual(t, "round-trip R2", result, live)
}

func TestSemanticApply_RoundTrip_R3_UserRemovesLeaf(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)
	ops := SemanticDiff(upstream, live)
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d", len(conflicts))
	}
	assertDeepEqual(t, "round-trip R3", result, live)
}

func TestSemanticApply_R1_SurvivesMaintainerChange(t *testing.T) {
	// Tester case + maintainer edit: user has the upload-tag-at-start,
	// AND the maintainer changed COMPLETION_ON deep inside the rule.
	// Both edits must land in the result, with zero conflicts (because
	// the user and maintainer edit different elements).
	upstream := jsonObj(t, `{
		"conditions": [
			{"operator":"OR","conditions":[
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}]},
			{"field":"NAME","operator":"MATCHES","value":"X"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
			{"operator":"OR","conditions":[
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}]},
			{"field":"NAME","operator":"MATCHES","value":"X"}]
	}`)
	userOps := SemanticDiff(upstream, live)

	// Maintainer edits the value inside the OR group.
	upstreamWithMaintainerEdit := jsonObj(t, `{
		"conditions": [
			{"operator":"OR","conditions":[
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}]},
			{"field":"NAME","operator":"MATCHES","value":"X"}]
	}`)
	// Expected: result = user's add-at-start PLUS maintainer's
	// updated COMPLETION_ON inside the OR group.
	expected := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
			{"operator":"OR","conditions":[
				{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}]},
			{"field":"NAME","operator":"MATCHES","value":"X"}]
	}`)

	result, conflicts := SemanticApply(upstreamWithMaintainerEdit, userOps)
	if len(conflicts) != 0 {
		t.Fatalf("R1 with maintainer edit: expected 0 conflicts (non-overlapping changes), got %d:\n%s",
			len(conflicts), formatConflicts(conflicts))
	}
	assertDeepEqual(t, "R1 with maintainer concurrent edit", result, expected)
}

// ---- R5 + R6 conflict detection ----

// R5 — Maintainer deletes element the user customized.
// User has a Replace op on a leaf; maintainer-side, the leaf is gone.
// Apply should produce a `target_gone` conflict at medium severity.
func TestSemanticApply_R5_MaintainerDeletesCustomizedLeaf(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}]
	}`)
	userOps := SemanticDiff(upstream, live) // 1 Replace op

	// Maintainer removed the leaf entirely.
	upstreamWithMaintainerDelete := jsonObj(t, `{
		"conditions": []
	}`)

	result, conflicts := SemanticApply(upstreamWithMaintainerDelete, userOps)
	if len(conflicts) != 1 {
		t.Fatalf("R5: expected exactly 1 conflict, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	c := conflicts[0]
	if c.Reason != ConflictTargetGone {
		t.Fatalf("R5: expected target_gone, got %s", c.Reason)
	}
	if c.Severity != SeverityMedium {
		t.Fatalf("R5: expected medium severity, got %s", c.Severity)
	}
	// Result should equal upstream-with-maintainer-delete (the op was
	// skipped because target gone — no destructive merge).
	assertDeepEqual(t, "R5 skipped op leaves maintainer state",
		result, jsonObj(t, `{"conditions": []}`))
}

// R6 — Maintainer changes a value the user already replaced.
// User's Replace targets fp(upstream-old-value). Maintainer modified
// the SAME field; upstream-now has a different value. Apply should
// surface `value_drifted` and skip the op (user's edit preserved on
// disk, conflict reported for review).
func TestSemanticApply_R6_BothSidesChangedSameField(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}]
	}`)
	userOps := SemanticDiff(upstream, live) // 1 Replace

	// Maintainer changed the same field to 1850000.
	upstreamWithMaintainerDrift := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1850000"}]
	}`)

	result, conflicts := SemanticApply(upstreamWithMaintainerDrift, userOps)
	if len(conflicts) != 1 {
		t.Fatalf("R6: expected exactly 1 conflict, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	c := conflicts[0]
	if c.Reason != ConflictValueDrifted {
		t.Fatalf("R6: expected value_drifted, got %s", c.Reason)
	}
	if c.Severity != SeverityMedium {
		t.Fatalf("R6: expected medium severity, got %s", c.Severity)
	}
	// Result keeps maintainer's value (op was skipped, drift surfaced for
	// review — user can re-capture or drop).
	assertDeepEqual(t, "R6 leaves maintainer's value in place",
		result, upstreamWithMaintainerDrift)
}

// R4 apply-side — Maintainer reorders an array; user has a Replace on
// one of the elements. Apply must still find the customized leaf by
// fingerprint and patch it in place, preserving maintainer's new order.
func TestSemanticApply_R4_MaintainerReorderPreservesUserReplace(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"},
			{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)
	// User modifies B → B-mod.
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B-mod"},
			{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)
	userOps := SemanticDiff(upstream, live) // 1 Replace on B's fp

	// Maintainer reorders.
	upstreamReordered := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"C"},
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"}]
	}`)
	// Expected: maintainer's order preserved, B → B-mod where it now lives.
	expected := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"C"},
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B-mod"}]
	}`)
	result, conflicts := SemanticApply(upstreamReordered, userOps)
	if len(conflicts) != 0 {
		t.Fatalf("R4 reorder: expected 0 conflicts, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	assertDeepEqual(t, "R4 reorder preserves user replace via fp", result, expected)
}

// ---- B3 from code review: PartialFingerprint avoids cross-operator false-match ----

// Without B3 fix, probeForDriftedLeaf would false-match an entirely
// different operator (e.g. GREATER_THAN target → LESS_THAN sibling
// with same field+value). With B3 we look up by partial fp, which
// includes operator+negate, so the lookup stays precise.
func TestSemanticApply_B3_PartialFp_AvoidsCrossOperatorFalseMatch(t *testing.T) {
	// User changes value on a GREATER_THAN leaf.
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1000"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"2000"}]
	}`)
	ops := SemanticDiff(upstream, live)
	if len(ops) != 1 || ops[0].Match == nil || ops[0].Match.Partial == "" {
		t.Fatalf("expected 1 Replace op with Match.Partial set, got %+v", ops)
	}

	// Maintainer replaced the leaf with a DIFFERENT-operator leaf
	// at same field+value-shape. Pre-B3 the probe would false-match
	// and produce a misleading value_drifted. Post-B3 partial-fp
	// includes operator → no match → target_gone (correct).
	upstreamNow := jsonObj(t, `{
		"conditions": [
			{"field":"COMPLETION_ON","operator":"LESS_THAN","value":"500"}]
	}`)
	_, conflicts := SemanticApply(upstreamNow, ops)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Reason != ConflictTargetGone {
		t.Fatalf("expected target_gone (different operator = different leaf), got %s",
			conflicts[0].Reason)
	}
}

// ---- Multi-add ordering regressions (B1 + B2 from code review) ----

// Two consecutive adds at the START of an array must round-trip in
// the captured order. Pre-fix this produced [B, A, X] instead of
// [A, B, X] because both ops resolved to Position{start} and apply
// did `insertAt(0, A); insertAt(0, B)`.
func TestSemanticApply_RoundTrip_TwoAddsAtStart_B1(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"X"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"NOT_EQUAL","value":"A_first"},
			{"field":"TAGS","operator":"NOT_EQUAL","value":"B_second"},
			{"field":"TAGS","operator":"EQUAL","value":"X"}]
	}`)
	ops := SemanticDiff(upstream, live)
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	assertDeepEqual(t, "B1 two adds at start round-trip", result, live)
}

// Two consecutive adds at the END of an array (B2 — same class).
func TestSemanticApply_RoundTrip_TwoAddsAtEnd_B2(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"X"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"X"},
			{"field":"TAGS","operator":"NOT_EQUAL","value":"A_first"},
			{"field":"TAGS","operator":"NOT_EQUAL","value":"B_second"}]
	}`)
	ops := SemanticDiff(upstream, live)
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	assertDeepEqual(t, "B2 two adds at end round-trip", result, live)
}

// Three adds in a row in the middle — exercises the "predecessor is
// an earlier add" anchor chain across multiple hops.
func TestSemanticApply_RoundTrip_ThreeAddsInMiddle(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"Z"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"NOT_EQUAL","value":"new1"},
			{"field":"TAGS","operator":"NOT_EQUAL","value":"new2"},
			{"field":"TAGS","operator":"NOT_EQUAL","value":"new3"},
			{"field":"TAGS","operator":"EQUAL","value":"Z"}]
	}`)
	ops := SemanticDiff(upstream, live)
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	assertDeepEqual(t, "three-add chain round-trip", result, live)
}

// ---- Object-key Replace compare-and-swap (B6 from code review) ----

// User renames a rule (object-key field, not array element). Maintainer
// hasn't touched the name → user's edit lands cleanly.
func TestSemanticApply_ObjectKeyReplace_NoDrift(t *testing.T) {
	upstream := jsonObj(t, `{"name": "old", "interval": 60}`)
	live := jsonObj(t, `{"name": "user-rename", "interval": 60}`)
	ops := SemanticDiff(upstream, live)
	result, conflicts := SemanticApply(upstream, ops)
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts on clean apply, got %d", len(conflicts))
	}
	assertDeepEqual(t, "object-key replace clean", result, live)
}

// User renamed the rule. Maintainer ALSO renamed it (different value).
// Apply must produce value_drifted and keep maintainer's value (user
// can re-capture or accept).
func TestSemanticApply_ObjectKeyReplace_DriftConflict_B6(t *testing.T) {
	upstreamAtCapture := jsonObj(t, `{"name": "old", "interval": 60}`)
	live := jsonObj(t, `{"name": "user-rename", "interval": 60}`)
	userOps := SemanticDiff(upstreamAtCapture, live)

	// Maintainer renamed to a different value.
	upstreamNow := jsonObj(t, `{"name": "maintainer-rename", "interval": 60}`)
	result, conflicts := SemanticApply(upstreamNow, userOps)

	if len(conflicts) != 1 {
		t.Fatalf("expected 1 value_drifted conflict, got %d:\n%s",
			len(conflicts), formatConflicts(conflicts))
	}
	if conflicts[0].Reason != ConflictValueDrifted {
		t.Fatalf("expected value_drifted, got %s", conflicts[0].Reason)
	}
	if conflicts[0].Severity != SeverityMedium {
		t.Fatalf("expected medium severity, got %s", conflicts[0].Severity)
	}
	// Maintainer's value preserved — op skipped.
	assertDeepEqual(t, "B6 leaves maintainer rename in place",
		result, upstreamNow)
}

// Object-key Replace where maintainer removed the key entirely →
// target_gone (existing behaviour, but worth a regression test).
func TestSemanticApply_ObjectKeyReplace_KeyRemoved(t *testing.T) {
	upstreamAtCapture := jsonObj(t, `{"name": "old", "interval": 60}`)
	live := jsonObj(t, `{"name": "user-rename", "interval": 60}`)
	userOps := SemanticDiff(upstreamAtCapture, live)

	// Maintainer deleted the "name" key.
	upstreamNow := jsonObj(t, `{"interval": 60}`)
	result, conflicts := SemanticApply(upstreamNow, userOps)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 target_gone conflict, got %d", len(conflicts))
	}
	if conflicts[0].Reason != ConflictTargetGone {
		t.Fatalf("expected target_gone, got %s", conflicts[0].Reason)
	}
	assertDeepEqual(t, "key removed leaves upstream-now state", result, upstreamNow)
}

// ---- Position-anchor edge cases ----

func TestSemanticApply_AnchorGone_FallsBackToEnd(t *testing.T) {
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
	userOps := SemanticDiff(upstream, live)
	// op[0] should be Add with Position.after=fp(A)

	// Maintainer removes A — the anchor for our Add op.
	upstreamWithoutAnchor := jsonObj(t, `{
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"C"}]
	}`)
	result, conflicts := SemanticApply(upstreamWithoutAnchor, userOps)

	// Expect: 1 conflict (anchor_gone, low severity), B appended at end.
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 anchor_gone conflict, got %d:\n%s", len(conflicts), formatConflicts(conflicts))
	}
	if conflicts[0].Reason != ConflictAnchorGone {
		t.Fatalf("expected ConflictAnchorGone, got %s", conflicts[0].Reason)
	}
	if conflicts[0].Severity != SeverityLow {
		t.Fatalf("expected low severity for anchor_gone, got %s", conflicts[0].Severity)
	}
	// B should be at the end of conditions.
	resultMap, _ := result.(map[string]any)
	conds, _ := resultMap["conditions"].([]any)
	if len(conds) != 2 {
		t.Fatalf("expected 2 conditions after fallback append, got %d", len(conds))
	}
	last, _ := conds[len(conds)-1].(map[string]any)
	if v, _ := last["value"].(string); v != "B" {
		t.Fatalf("expected B at end after anchor-gone fallback, got %v", last)
	}
}

// ---- Apply doesn't mutate the input ----

func TestSemanticApply_DoesNotMutateInput(t *testing.T) {
	upstream := jsonObj(t, `{
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)
	live := jsonObj(t, `{
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"A"},
			{"field":"TAGS","operator":"EQUAL","value":"B"}]
	}`)
	upstreamSnapshot, _ := json.Marshal(upstream)
	ops := SemanticDiff(upstream, live)
	_, _ = SemanticApply(upstream, ops)
	upstreamAfter, _ := json.Marshal(upstream)
	if string(upstreamSnapshot) != string(upstreamAfter) {
		t.Fatalf("apply mutated input upstream:\n  before: %s\n  after:  %s",
			upstreamSnapshot, upstreamAfter)
	}
}

// ---- helpers ----

func assertDeepEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("%s: deep-equal mismatch\n  got:  %s\n  want: %s", label, gb, wb)
	}
}

func formatConflicts(cs []OpConflict) string {
	b, _ := json.MarshalIndent(cs, "  ", "  ")
	return string(b)
}
