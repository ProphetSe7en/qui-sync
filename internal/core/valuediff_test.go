package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

func TestDiffValueLinesConditions(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     []string
		absent   []string
	}{
		{
			name: "threshold changed on one condition",
			old:  `{"conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":2}]}`,
			new:  `{"conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":3}]}`,
			want: []string{"RATIO GREATER_THAN 2", "2 → 3"},
		},
		{
			name: "condition added in the middle keeps the others quiet",
			old:  `{"conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":2},{"field":"SEEDING_TIME","operator":"GREATER_THAN","value":100}]}`,
			new:  `{"conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":2},{"field":"TAGS","operator":"EQUAL","value":"Tier2"},{"field":"SEEDING_TIME","operator":"GREATER_THAN","value":100}]}`,
			want: []string{"added: TAGS EQUAL Tier2"},
			// The untouched conditions must not show up as moved.
			absent: []string{"SEEDING_TIME GREATER_THAN 100:", "RATIO GREATER_THAN 2:"},
		},
		{
			name: "condition removed says what it was",
			old:  `{"conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":2},{"field":"TAGS","operator":"EQUAL","value":"Tier2"}]}`,
			new:  `{"conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":2}]}`,
			want: []string{"removed: TAGS EQUAL Tier2"},
		},
		{
			name: "mode changed deeper in the tree",
			old:  `{"delete":{"enabled":true,"mode":"deleteWithFiles"}}`,
			new:  `{"delete":{"enabled":true,"mode":"delete"}}`,
			want: []string{"delete.mode: deleteWithFiles → delete"},
		},
		{
			name: "a whole action block added",
			old:  `{"schemaVersion":"1"}`,
			new:  `{"schemaVersion":"1","tag":{"enabled":true,"mode":"full"}}`,
			want: []string{"tag added:"},
		},
		{
			// Negating a condition inverts what the rule does, so it reads
			// as the old one going and a new one arriving rather than as a
			// small edit. Both lines name the condition in full, which is
			// what a reviewer scanning a preview needs to see.
			name: "negating a condition shows both forms",
			old:  `{"conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier2"}]}`,
			new:  `{"conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier2","negate":true}]}`,
			want: []string{"removed: TAGS EQUAL Tier2", "added: not TAGS EQUAL Tier2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := diffValueLines(decodeJSON(t, tc.old), decodeJSON(t, tc.new))
			got := joinLines(lines)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in:\n%s", a, got)
				}
			}
		})
	}
}

func TestDiffValueLinesEqualIsEmpty(t *testing.T) {
	v := `{"delete":{"enabled":true,"condition":{"operator":"AND","conditions":[{"field":"RATIO","operator":"GREATER_THAN","value":2}]}}}`
	if lines := diffValueLines(decodeJSON(t, v), decodeJSON(t, v)); len(lines) != 0 {
		t.Errorf("identical values produced %v", lines)
	}
}

// A rule rewritten wholesale should not dump its entire tree into a
// preview row.
func TestDiffValueLinesIsCapped(t *testing.T) {
	var oldB, newB strings.Builder
	oldB.WriteString(`{"conditions":[`)
	newB.WriteString(`{"conditions":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			oldB.WriteString(",")
			newB.WriteString(",")
		}
		oldB.WriteString(`{"field":"F` + string(rune('A'+i%26)) + `","operator":"EQUAL","value":1}`)
		newB.WriteString(`{"field":"F` + string(rune('A'+i%26)) + `","operator":"EQUAL","value":2}`)
	}
	oldB.WriteString(`]}`)
	newB.WriteString(`]}`)

	lines := diffValueLines(decodeJSON(t, oldB.String()), decodeJSON(t, newB.String()))
	if len(lines) != maxDiffLines+1 {
		t.Fatalf("got %d lines, want %d plus a summary line", len(lines), maxDiffLines)
	}
	if !strings.HasPrefix(lines[len(lines)-1], "and ") {
		t.Errorf("last line should summarise the rest, got %q", lines[len(lines)-1])
	}
}

func TestShortValueRendersConditionLeaves(t *testing.T) {
	cases := map[string]string{
		`{"field":"TAGS","operator":"EQUAL","value":"Tier2"}`:           "TAGS EQUAL Tier2",
		`{"field":"RATIO","operator":"GREATER_THAN","value":2}`:         "RATIO GREATER_THAN 2",
		`{"field":"TAGS","operator":"EQUAL","value":"x","negate":true}`: "not TAGS EQUAL x",
		`{"enabled":true,"mode":"full"}`:                                `{"enabled":true,"mode":"full"}`,
		`"plain string"`:                                                "plain string",
		`17`:                                                            "17",
	}
	for in, want := range cases {
		var v any
		if err := json.Unmarshal([]byte(in), &v); err != nil {
			t.Fatalf("decode %s: %v", in, err)
		}
		if got := shortValue(v); got != want {
			t.Errorf("shortValue(%s) = %q, want %q", in, got, want)
		}
	}
}
