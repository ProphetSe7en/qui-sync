package customize

import (
	"encoding/json"
	"strings"
	"testing"
)

// Phase A smoke test: every Op kind round-trips through JSON without
// losing or rearranging fields. Locks the on-disk shape down before
// Phase B starts emitting these via SemanticDiff.

func TestOp_JSONRoundTrip_Add(t *testing.T) {
	original := Op{
		Kind: OpAdd,
		Path: []string{"conditions", "delete", "condition", "conditions"},
		Position: &Position{
			Kind:        "after",
			Anchor:      "abc123def456",
			FallbackEnd: true,
		},
		Value: map[string]any{
			"field":    "TAGS",
			"operator": "NOT_EQUAL",
			"value":    "upload",
		},
	}
	roundtrip(t, original)
}

func TestOp_JSONRoundTrip_Remove(t *testing.T) {
	original := Op{
		Kind: OpRemove,
		Path: []string{"conditions", "delete", "condition", "conditions"},
		Match: &MatchSpec{
			Fingerprint: "abc123def456",
		},
	}
	roundtrip(t, original)
}

func TestOp_JSONRoundTrip_Replace(t *testing.T) {
	original := Op{
		Kind:  OpReplace,
		Path:  []string{"conditions", "delete", "condition", "conditions"},
		Match: &MatchSpec{Fingerprint: "abc123def456"},
		Field: "value",
		Value: "1843200",
	}
	roundtrip(t, original)
}

func TestOp_JSONRoundTrip_Descend(t *testing.T) {
	original := Op{
		Kind:  OpDescend,
		Path:  []string{"conditions", "delete", "condition", "conditions"},
		Match: &MatchSpec{Fingerprint: "abc123def456"},
		SubOps: []Op{
			{
				Kind:  OpReplace,
				Path:  []string{"conditions"},
				Match: &MatchSpec{Fingerprint: "inner999"},
				Field: "value",
				Value: "21600",
			},
		},
	}
	roundtrip(t, original)
}

func TestOp_JSONOmitempty(t *testing.T) {
	// Remove op should not emit position/value/field/sub_ops fields.
	op := Op{
		Kind:  OpRemove,
		Path:  []string{"conditions"},
		Match: &MatchSpec{Fingerprint: "fp1"},
	}
	b, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, forbidden := range []string{`"position"`, `"value"`, `"field"`, `"sub_ops"`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("Remove op JSON contains %s — omitempty broken: %s", forbidden, s)
		}
	}
}

func TestCustomizationV2_JSONShape(t *testing.T) {
	// Locks the on-disk envelope so a later schemaVersion rename doesn't
	// silently happen.
	c := CustomizationV2{
		SchemaVersion:       CurrentSchemaVersion,
		RuleSchemaVersion:   "1",
		CapturedAt:          "2026-05-25T14:30:00Z",
		UpstreamFingerprint: "abc123",
		Notes:               "added upload-exclude",
		Ops: []Op{
			{Kind: OpAdd, Path: []string{"x"}, Position: &Position{Kind: "end", FallbackEnd: true}, Value: "v"},
		},
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, must := range []string{
		`"schemaVersion": "2"`,
		`"ruleSchemaVersion": "1"`,
		`"capturedAt"`,
		`"upstreamFingerprint"`,
		`"ops"`,
	} {
		if !strings.Contains(s, must) {
			t.Errorf("expected on-disk JSON to contain %s, got:\n%s", must, s)
		}
	}
}

func roundtrip(t *testing.T, op Op) {
	t.Helper()
	b, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Op
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v\nJSON: %s", err, b)
	}
	b2, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(b) != string(b2) {
		t.Errorf("round-trip not stable:\n  first:  %s\n  second: %s", b, b2)
	}
}
