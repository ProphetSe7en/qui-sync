package customize

import (
	"encoding/json"
	"testing"
)

// DiffBridge must strip server-assigned noise + user-owned
// auto-preserved fields before computing the diff (v0.5 parity).
// Without this, the op list would either flood with server-metadata
// mismatches or capture trackerPattern/enabled changes that Layer 3
// is supposed to preserve separately.

func TestDiffBridge_StripsNoiseFields(t *testing.T) {
	// Upstream is the rule as it lives in the repo (no server
	// metadata, has _slug + _description).
	upstream := []byte(`{
		"_slug": "tag-tier1",
		"_description": "Some maintainer note",
		"name": "Tag: Tier 1",
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)
	// Live is what Qui's API returned: server has stamped id /
	// createdAt / updatedAt / instanceId, dropped _slug + _description.
	live := []byte(`{
		"id": 42,
		"instanceId": 1,
		"createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-05-26T12:00:00Z",
		"name": "Tag: Tier 1",
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)
	ops, err := DiffBridge(upstream, live)
	if err != nil {
		t.Fatalf("DiffBridge: %v", err)
	}
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops (only noise differs), got %d:\n%s", len(ops), formatOpsForTest(ops))
	}
}

func TestDiffBridge_StripsAutoPreserved(t *testing.T) {
	// User-owned fields (trackerPattern, enabled, intervalSeconds)
	// MUST be ignored by capture — Layer 3 handles them. Capturing
	// them here would freeze the user's setting against future
	// upstream changes.
	upstream := []byte(`{
		"trackerPattern": "*",
		"enabled": true,
		"intervalSeconds": 60,
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"X"}]
	}`)
	live := []byte(`{
		"trackerPattern": "tracker.user.example",
		"enabled": false,
		"intervalSeconds": 300,
		"conditions": [{"field":"TAGS","operator":"EQUAL","value":"X"}]
	}`)
	ops, err := DiffBridge(upstream, live)
	if err != nil {
		t.Fatalf("DiffBridge: %v", err)
	}
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops (only auto-preserved differ), got %d:\n%s", len(ops), formatOpsForTest(ops))
	}
}

func TestDiffBridge_FindsRealChangesUnderNoise(t *testing.T) {
	// Both noise mismatches AND a real change in the rule body.
	// Real change should surface; noise should be silent.
	upstream := []byte(`{
		"_slug": "tag-tier1",
		"name": "Old Name",
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"original"}]
	}`)
	live := []byte(`{
		"id": 42,
		"createdAt": "2026-01-01T00:00:00Z",
		"name": "User Renamed",
		"conditions": [
			{"field":"TAGS","operator":"EQUAL","value":"original"}]
	}`)
	ops, err := DiffBridge(upstream, live)
	if err != nil {
		t.Fatalf("DiffBridge: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 op (name change), got %d:\n%s", len(ops), formatOpsForTest(ops))
	}
	if ops[0].Kind != OpReplace || ops[0].Field != "name" {
		t.Fatalf("expected Replace on 'name', got Kind=%s Field=%q", ops[0].Kind, ops[0].Field)
	}
	if ops[0].Value != "User Renamed" {
		t.Fatalf("expected new value 'User Renamed', got %v", ops[0].Value)
	}
}

func TestDiffBridge_ReturnsNilOnNoChanges(t *testing.T) {
	rule := []byte(`{"name":"X","conditions":[]}`)
	ops, err := DiffBridge(rule, rule)
	if err != nil {
		t.Fatalf("DiffBridge: %v", err)
	}
	if ops != nil {
		t.Fatalf("expected nil ops, got %d", len(ops))
	}
}

// ApplyBridge sanity: round-trip via JSON bytes.
func TestApplyBridge_RoundTrip(t *testing.T) {
	upstream := []byte(`{
		"name":"X",
		"conditions":[
			{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)
	live := []byte(`{
		"name":"X",
		"conditions":[
			{"field":"TAGS","operator":"NOT_EQUAL","value":"upload"},
			{"field":"TAGS","operator":"EQUAL","value":"A"}]
	}`)
	ops, err := DiffBridge(upstream, live)
	if err != nil {
		t.Fatalf("DiffBridge: %v", err)
	}
	c := &CustomizationV2{Ops: ops}
	out, conflicts, err := ApplyBridge(upstream, c)
	if err != nil {
		t.Fatalf("ApplyBridge: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d", len(conflicts))
	}
	// Should equal live (modulo top-level field order — compare via
	// re-decode + DeepEqual on the maps).
	var gotObj, wantObj any
	_ = json.Unmarshal(out, &gotObj)
	_ = json.Unmarshal(live, &wantObj)
	gb, _ := json.Marshal(gotObj)
	wb, _ := json.Marshal(wantObj)
	if string(gb) != string(wb) {
		t.Fatalf("round-trip mismatch:\n  got:  %s\n  want: %s", gb, wb)
	}
}

func formatOpsForTest(ops []Op) string {
	b, _ := json.MarshalIndent(ops, "  ", "  ")
	return string(b)
}
