package customize

import (
	"encoding/json"
	"testing"
)

// Phase B tests for fingerprint.go. Cover the 5 fingerprint properties
// from design §9.1.

func TestFingerprint_LeafDeterministic(t *testing.T) {
	a := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"Tier1"}`)
	b := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"Tier1"}`)
	if Compute(a) != Compute(b) {
		t.Fatalf("same content → different fingerprint: %s vs %s", Compute(a), Compute(b))
	}
	if Compute(a) == "" {
		t.Fatalf("empty fingerprint for valid leaf")
	}
}

func TestFingerprint_DifferentValueDiffers(t *testing.T) {
	a := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"Tier1"}`)
	b := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"noHL"}`)
	if Compute(a) == Compute(b) {
		t.Fatalf("different value → same fingerprint — collision")
	}
}

func TestFingerprint_NegateMatters(t *testing.T) {
	without := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"x"}`)
	withNeg := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"x","negate":true}`)
	if Compute(without) == Compute(withNeg) {
		t.Fatalf("negate=true must differ from negate=false")
	}
}

func TestFingerprint_GroupStableUnderChildReorder(t *testing.T) {
	// Two groups with same children in different orders should hash equal —
	// that's the property that makes maintainer reorder invisible.
	g1 := jsonObj(t, `{"operator":"AND","conditions":[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"}]}`)
	g2 := jsonObj(t, `{"operator":"AND","conditions":[
		{"field":"TAGS","operator":"EQUAL","value":"B"},
		{"field":"TAGS","operator":"EQUAL","value":"A"}]}`)
	if Compute(g1) != Compute(g2) {
		t.Fatalf("child reorder must NOT change group fingerprint")
	}
}

func TestFingerprint_GroupContentMatters(t *testing.T) {
	// Same operator, but different children — must differ.
	a := jsonObj(t, `{"operator":"AND","conditions":[
		{"field":"TAGS","operator":"EQUAL","value":"A"}]}`)
	b := jsonObj(t, `{"operator":"AND","conditions":[
		{"field":"TAGS","operator":"EQUAL","value":"X"}]}`)
	if Compute(a) == Compute(b) {
		t.Fatalf("different child content → same fingerprint")
	}
}

func TestFingerprint_GroupOperatorMatters(t *testing.T) {
	// AND vs OR with identical children — must differ.
	andGroup := jsonObj(t, `{"operator":"AND","conditions":[
		{"field":"TAGS","operator":"EQUAL","value":"A"}]}`)
	orGroup := jsonObj(t, `{"operator":"OR","conditions":[
		{"field":"TAGS","operator":"EQUAL","value":"A"}]}`)
	if Compute(andGroup) == Compute(orGroup) {
		t.Fatalf("AND vs OR with same children — fingerprint must differ")
	}
}

// ---- Partial fingerprint coverage ----

func TestPartial_IgnoresValue(t *testing.T) {
	// Two leaves with same field+operator+negate but different values
	// share a Partial fingerprint — that's what Pass 2 matching needs.
	a := jsonObj(t, `{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}`)
	b := jsonObj(t, `{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}`)
	if Partial(a) != Partial(b) {
		t.Fatalf("different value → different partial fp: %s vs %s", Partial(a), Partial(b))
	}
	if Compute(a) == Compute(b) {
		t.Fatalf("full fp must still differ even when partial fp matches")
	}
}

func TestPartial_GroupReturnsEmpty(t *testing.T) {
	g := jsonObj(t, `{"operator":"AND","conditions":[{"field":"TAGS","operator":"EQUAL","value":"x"}]}`)
	if Partial(g) != "" {
		t.Fatalf("Partial on a group must return empty (groups don't participate in Pass 2)")
	}
}

func TestPartial_DistinctFromFull(t *testing.T) {
	// Partial fp and full fp on the SAME leaf must never collide.
	leaf := jsonObj(t, `{"field":"TAGS","operator":"EQUAL","value":"Tier1"}`)
	if Partial(leaf) == Compute(leaf) {
		t.Fatalf("partial and full fp collide on same leaf — bucket-cross risk")
	}
}

// ---- Shape predicates ----

func TestIsLeaf_True(t *testing.T) {
	if !isLeaf(jsonObj(t, `{"field":"X","operator":"Y","value":"Z"}`)) {
		t.Fatalf("leaf-shape not recognised")
	}
}

func TestIsLeaf_FalseForGroup(t *testing.T) {
	if isLeaf(jsonObj(t, `{"operator":"AND","conditions":[]}`)) {
		t.Fatalf("group misclassified as leaf")
	}
}

func TestIsGroup_True(t *testing.T) {
	if !isGroup(jsonObj(t, `{"operator":"OR","conditions":[]}`)) {
		t.Fatalf("group-shape not recognised")
	}
}

func TestIsGroup_FalseForLeaf(t *testing.T) {
	if isGroup(jsonObj(t, `{"field":"X","operator":"Y","value":"Z"}`)) {
		t.Fatalf("leaf misclassified as group")
	}
}

func TestIsLeaf_NilSafe(t *testing.T) {
	if isLeaf(nil) || isGroup(nil) {
		t.Fatalf("nil should not classify as either leaf or group")
	}
}

func TestCompute_NilReturnsEmpty(t *testing.T) {
	if Compute(nil) != "" {
		t.Fatalf("Compute(nil) must return empty fingerprint")
	}
}

// ---- helpers ----

func jsonObj(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("test JSON parse failed: %v", err)
	}
	return v
}
