package customize

import (
	"encoding/json"
	"sort"
	"testing"
)

// Phase B tests for match.go. Cover the 8 match cases from design §9.2:
// disjoint, identical, add-at-start, add-at-end, add-in-middle,
// duplicate fingerprints, one-removed, mixed shuffle.

func TestMatch_DisjointValuesSameKey(t *testing.T) {
	// Leaves with same field+operator+negate but different value share
	// a partial fp — Pass 2 pairs them as a value-modification (NOT as
	// add+remove). This is the documented Pass 2 behaviour and the whole
	// point of the value-drifted detection path.
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"B"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 0 {
		t.Fatalf("full fp differs — identity pass must not pair: %+v", r.IdentityPairs)
	}
	if len(r.ModifyPairs) != 1 {
		t.Fatalf("expected 1 value-modification pair, got %d", len(r.ModifyPairs))
	}
	if len(r.AddedInLive) != 0 || len(r.RemovedFromLive) != 0 {
		t.Fatalf("with partial-fp match, added/removed must be empty: added=%v removed=%v",
			r.AddedInLive, r.RemovedFromLive)
	}
}

func TestMatch_TrulyDisjoint(t *testing.T) {
	// Different field altogether — partial fp also won't match.
	up := jsonArr(t, `[{"field":"TAGS","operator":"EQUAL","value":"A"}]`)
	lv := jsonArr(t, `[{"field":"NAME","operator":"MATCHES","value":"X"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 0 || len(r.ModifyPairs) != 0 {
		t.Fatalf("truly disjoint must produce no pairs: %+v", r)
	}
	if len(r.AddedInLive) != 1 || len(r.RemovedFromLive) != 1 {
		t.Fatalf("expected 1 add + 1 remove, got added=%v removed=%v", r.AddedInLive, r.RemovedFromLive)
	}
}

func TestMatch_Identical(t *testing.T) {
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 2 {
		t.Fatalf("expected 2 identity pairs, got %d (%+v)", len(r.IdentityPairs), r.IdentityPairs)
	}
	if len(r.ModifyPairs) != 0 || len(r.AddedInLive) != 0 || len(r.RemovedFromLive) != 0 {
		t.Fatalf("identical arrays should produce ONLY identity pairs: %+v", r)
	}
}

func TestMatch_AddAtStart_TesterCase(t *testing.T) {
	// The actual bug-reproducing scenario: user inserts TAGS NOT_EQUAL upload
	// at the START, pushing the OR-block and NAME-block down. With v0.5
	// (JSON Patch) this exploded into 12 replace ops. The new matcher
	// must produce a clean 1 add + 2 identity pairs.
	up := jsonArr(t, `[
		{"operator":"OR","conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier1"}]},
		{"field":"NAME","operator":"MATCHES","value":"S\\d+"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
		{"operator":"OR","conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier1"}]},
		{"field":"NAME","operator":"MATCHES","value":"S\\d+"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 2 {
		t.Fatalf("expected 2 identity pairs (OR-block, NAME-leaf), got %d", len(r.IdentityPairs))
	}
	if len(r.AddedInLive) != 1 || r.AddedInLive[0] != 0 {
		t.Fatalf("expected 1 add at lv[0] (the upload-tag), got added=%v", r.AddedInLive)
	}
	if len(r.RemovedFromLive) != 0 || len(r.ModifyPairs) != 0 {
		t.Fatalf("nothing should be removed or modified: %+v", r)
	}
}

func TestMatch_AddAtEnd(t *testing.T) {
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"NAME","operator":"MATCHES","value":"X"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 1 {
		t.Fatalf("expected 1 identity pair, got %d", len(r.IdentityPairs))
	}
	if len(r.AddedInLive) != 1 || r.AddedInLive[0] != 1 {
		t.Fatalf("expected 1 add at lv[1], got %v", r.AddedInLive)
	}
}

func TestMatch_AddInMiddle(t *testing.T) {
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"C"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"},
		{"field":"TAGS","operator":"EQUAL","value":"C"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 2 {
		t.Fatalf("expected 2 identity pairs, got %d", len(r.IdentityPairs))
	}
	if len(r.AddedInLive) != 1 || r.AddedInLive[0] != 1 {
		t.Fatalf("expected 1 add at lv[1], got %v", r.AddedInLive)
	}
	if len(r.RemovedFromLive) != 0 || len(r.ModifyPairs) != 0 {
		t.Fatalf("nothing else should be flagged: %+v", r)
	}
}

func TestMatch_DuplicateFingerprints(t *testing.T) {
	// Two leaves with identical (field,operator,value,negate) on both sides.
	// Should pair n-to-n in order, not double-pair, not miss.
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"X"},
		{"field":"TAGS","operator":"EQUAL","value":"X"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"X"},
		{"field":"TAGS","operator":"EQUAL","value":"X"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 2 {
		t.Fatalf("expected 2 identity pairs (duplicates pair n-to-n), got %d", len(r.IdentityPairs))
	}
	if len(r.AddedInLive) != 0 || len(r.RemovedFromLive) != 0 {
		t.Fatalf("nothing should be added/removed: %+v", r)
	}
	// Verify no upIdx or lvIdx is reused.
	upIdxs := map[int]int{}
	lvIdxs := map[int]int{}
	for _, p := range r.IdentityPairs {
		upIdxs[p.UpIdx]++
		lvIdxs[p.LvIdx]++
	}
	for k, v := range upIdxs {
		if v > 1 {
			t.Fatalf("upIdx %d paired %d times — duplicate match", k, v)
		}
	}
	for k, v := range lvIdxs {
		if v > 1 {
			t.Fatalf("lvIdx %d paired %d times — duplicate match", k, v)
		}
	}
}

func TestMatch_OneRemoved(t *testing.T) {
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 1 {
		t.Fatalf("expected 1 identity pair, got %d", len(r.IdentityPairs))
	}
	if len(r.RemovedFromLive) != 1 || r.RemovedFromLive[0] != 1 {
		t.Fatalf("expected 1 remove at up[1], got %v", r.RemovedFromLive)
	}
}

func TestMatch_ValueModificationPair(t *testing.T) {
	// User modifies value on a single leaf: same (field, operator, negate),
	// different value. Pass 1 misses it; Pass 2 catches it as a modify pair.
	up := jsonArr(t, `[
		{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"}]`)
	lv := jsonArr(t, `[
		{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 0 {
		t.Fatalf("identity pass should miss (full fp differs): %+v", r.IdentityPairs)
	}
	if len(r.ModifyPairs) != 1 {
		t.Fatalf("modify pass should pair once, got %d", len(r.ModifyPairs))
	}
	if len(r.AddedInLive) != 0 || len(r.RemovedFromLive) != 0 {
		t.Fatalf("mod-pair should not leave added/removed entries: %+v", r)
	}
}

func TestMatch_MixedShuffleAddRemove(t *testing.T) {
	// Upstream:  [A, B, C, D]
	// Live:      [B, A, E, D]   (A,B reordered; C removed; E added; D kept)
	// Expected: 3 identity pairs (A↔A, B↔B, D↔D), 1 add (E), 1 remove (C).
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"},
		{"field":"TAGS","operator":"EQUAL","value":"C"},
		{"field":"TAGS","operator":"EQUAL","value":"D"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"B"},
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"E"},
		{"field":"TAGS","operator":"EQUAL","value":"D"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 3 {
		t.Fatalf("expected 3 identity pairs (A,B,D), got %d", len(r.IdentityPairs))
	}
	// Without a 4th element to partial-match against, C and E should be
	// classified as remove + add respectively (NOT a modify pair —
	// different field+operator+negate-key would make them pair, but here
	// they share key, so they pair via Pass 2).
	// Re-check: C and E both have field=TAGS, operator=EQUAL, negate=false
	// (default). Same partial key → Pass 2 pairs them as value-mod.
	// So expected: 3 identity + 1 modify, 0 added, 0 removed.
	if len(r.ModifyPairs) != 1 {
		t.Fatalf("expected 1 modify pair (C↔E via partial-fp), got %d", len(r.ModifyPairs))
	}
	if len(r.AddedInLive) != 0 || len(r.RemovedFromLive) != 0 {
		t.Fatalf("no leftover added/removed: %+v", r)
	}
}

func TestMatch_AddAndRemoveDistinctKeys(t *testing.T) {
	// Distinct partial keys → no Pass 2 pairing → clean add/remove split.
	up := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"NAME","operator":"MATCHES","value":"removed"}]`)
	lv := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"100"}]`)
	r := matchArrays(up, lv)

	if len(r.IdentityPairs) != 1 {
		t.Fatalf("expected 1 identity pair, got %d", len(r.IdentityPairs))
	}
	if len(r.ModifyPairs) != 0 {
		t.Fatalf("different fields can't be modify-pair, got %d", len(r.ModifyPairs))
	}
	if len(r.AddedInLive) != 1 || len(r.RemovedFromLive) != 1 {
		t.Fatalf("expected 1 add + 1 remove, got added=%v removed=%v", r.AddedInLive, r.RemovedFromLive)
	}
}

// ---- lookup helpers ----

func TestFindByFingerprint_Unique(t *testing.T) {
	arr := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"A"},
		{"field":"TAGS","operator":"EQUAL","value":"B"}]`)
	fp := Compute(arr[1])
	idx, count := findByFingerprint(arr, fp)
	if idx != 1 || count != 1 {
		t.Fatalf("expected unique match at [1], got idx=%d count=%d", idx, count)
	}
}

func TestFindByFingerprint_NotFound(t *testing.T) {
	arr := jsonArr(t, `[{"field":"TAGS","operator":"EQUAL","value":"A"}]`)
	idx, count := findByFingerprint(arr, "deadbeef00000000")
	if idx != -1 || count != 0 {
		t.Fatalf("expected -1/0, got idx=%d count=%d", idx, count)
	}
}

func TestFindByFingerprint_Duplicates(t *testing.T) {
	arr := jsonArr(t, `[
		{"field":"TAGS","operator":"EQUAL","value":"X"},
		{"field":"TAGS","operator":"EQUAL","value":"X"}]`)
	fp := Compute(arr[0])
	idx, count := findByFingerprint(arr, fp)
	if idx != 0 || count != 2 {
		t.Fatalf("expected first=0 count=2 for duplicates, got idx=%d count=%d", idx, count)
	}
}

func TestFindByPartialFingerprint(t *testing.T) {
	arr := jsonArr(t, `[
		{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1836000"},
		{"field":"COMPLETION_ON","operator":"GREATER_THAN","value":"1843200"}]`)
	pfp := Partial(arr[0])
	idx := findByPartialFingerprint(arr, pfp)
	if idx != 0 {
		t.Fatalf("expected partial match at [0], got %d", idx)
	}
	// Both leaves have same partial fp; first wins.
	if Partial(arr[1]) != pfp {
		t.Fatalf("test setup broken — partial fp should match")
	}
}

// ---- helpers ----

func jsonArr(t *testing.T, s string) []any {
	t.Helper()
	var v []any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("test JSON parse failed: %v", err)
	}
	return v
}

// assertSorted is unused but kept here as a hook for property tests
// (Phase D will need it when verifying conflict ordering).
var _ = sort.IntsAreSorted
