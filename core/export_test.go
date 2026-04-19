package core

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// TestBuildRuleFileFieldOrder locks down the on-disk field ordering so
// it matches Qui's natural reading order. qui-sync used to emit keys
// alphabetically (Go's default map encoding) which scrambled the
// community-convention layout ("name" first, trackers, then conditions)
// and made diffs against other maintainers' repos noisy. The struct-based
// emitter in buildRuleFile guarantees a stable order — this test catches
// any refactor that accidentally reverts to map-based encoding.
func TestBuildRuleFileFieldOrder(t *testing.T) {
	raw := json.RawMessage(`{
		"id": 42,
		"instanceId": 2,
		"createdAt": "2026-04-19T00:00:00Z",
		"updatedAt": "2026-04-19T00:00:00Z",
		"name": "Tag: Tier 1",
		"enabled": true,
		"trackerPattern": "real-tracker.com",
		"trackerDomains": ["tracker-a.com", "tracker-b.net"],
		"conditions": {"schemaVersion": "1", "tag": {"enabled": true, "mode": "full", "tags": ["Tier1"]}},
		"intervalSeconds": 300,
		"dryRun": false,
		"notify": false
	}`)
	automation := Automation{ID: 42, Name: "Tag: Tier 1", Raw: raw}

	out, err := buildRuleFile(automation, "tag-tier1", &Config{}, 5)
	if err != nil {
		t.Fatalf("buildRuleFile: %v", err)
	}

	// Extract the top-level keys in the order they appear in the output.
	// Top-level keys are indented at two spaces and begin with a quote.
	topLevelKey := regexp.MustCompile(`(?m)^  "([^"]+)":`)
	matches := topLevelKey.FindAllStringSubmatch(string(out), -1)
	var got []string
	for _, m := range matches {
		got = append(got, m[1])
	}

	// Expected order matches the community convention and Qui's natural
	// response shape — identity first, trackers, then conditions, then
	// interval. qui-sync's own metadata (_slug) prefixes everything.
	want := []string{
		"_slug",
		"name",
		"sortOrder",
		"trackerPattern",
		"trackerDomains",
		"conditions",
		"intervalSeconds",
	}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("top-level field order regressed.\n got: %v\nwant: %v\n\nfull output:\n%s",
			got, want, string(out))
	}
}

// TestBuildRuleFilePreservesConditionsBytes confirms that the conditions
// block is passed through byte-for-byte — Qui's semantic ordering inside
// (schemaVersion first, action sub-blocks' "enabled"/"mode" before the
// predicate, etc.) must survive the export round-trip so diffs are
// noise-free when consumers sync.
func TestBuildRuleFilePreservesConditionsBytes(t *testing.T) {
	// Crafted to highlight non-alphabetical inner ordering that Qui uses:
	// "schemaVersion" before "delete", and within the nested "condition"
	// block "operator" before "conditions".
	innerConditions := `{"schemaVersion":"1","delete":{"enabled":true,"mode":"deleteWithFiles","condition":{"operator":"AND","conditions":[{"field":"TAGS","operator":"EQUAL","value":"Tier2"}]}}}`
	raw := json.RawMessage(`{
		"name": "Delete: Tier 2",
		"trackerPattern": "*",
		"trackerDomains": ["*"],
		"conditions": ` + innerConditions + `,
		"intervalSeconds": 21600
	}`)
	automation := Automation{ID: 1, Name: "Delete: Tier 2", Raw: raw}

	out, err := buildRuleFile(automation, "delete-tier2", &Config{}, 1)
	if err != nil {
		t.Fatalf("buildRuleFile: %v", err)
	}

	// The conditions block in the output should contain the exact same
	// key ordering Qui sent. Check by looking for specific substrings
	// that confirm the non-alphabetical sequence survived.
	outStr := string(out)

	// "schemaVersion" must appear BEFORE "delete" inside conditions.
	schemaIdx := strings.Index(outStr, `"schemaVersion"`)
	deleteIdx := strings.Index(outStr, `"delete":`)
	if schemaIdx < 0 || deleteIdx < 0 || schemaIdx > deleteIdx {
		t.Errorf("schemaVersion should precede delete inside conditions; got schema@%d delete@%d\n%s",
			schemaIdx, deleteIdx, outStr)
	}

	// Within "delete", "enabled" and "mode" should appear BEFORE "condition".
	enabledIdx := strings.Index(outStr, `"enabled": true`)
	conditionIdx := strings.Index(outStr, `"condition":`)
	if enabledIdx < 0 || conditionIdx < 0 || enabledIdx > conditionIdx {
		t.Errorf("enabled should precede condition; got enabled@%d condition@%d",
			enabledIdx, conditionIdx)
	}

	// Within the predicate, "operator" should appear BEFORE "conditions".
	opIdx := strings.Index(outStr, `"operator": "AND"`)
	innerCondIdx := strings.Index(outStr, `"conditions": [`)
	if opIdx < 0 || innerCondIdx < 0 || opIdx > innerCondIdx {
		t.Errorf("operator should precede conditions array; got operator@%d conditions@%d",
			opIdx, innerCondIdx)
	}
}

// TestBuildRuleFileStripsPersonalFields confirms that tracker values are
// replaced with placeholders and id/enabled/timestamps are never emitted.
func TestBuildRuleFileStripsPersonalFields(t *testing.T) {
	raw := json.RawMessage(`{
		"id": 42,
		"instanceId": 2,
		"name": "Tag: Tier 1",
		"enabled": true,
		"trackerPattern": "real-tracker.com",
		"trackerDomains": ["tracker-a.com"],
		"conditions": {"schemaVersion": "1"}
	}`)
	automation := Automation{ID: 42, Name: "Tag: Tier 1", Raw: raw}

	out, err := buildRuleFile(automation, "tag-tier1", &Config{}, 1)
	if err != nil {
		t.Fatalf("buildRuleFile: %v", err)
	}

	outStr := string(out)
	for _, forbidden := range []string{`"id":`, `"instanceId":`, `"enabled":`, `real-tracker.com`, `tracker-a.com`} {
		if strings.Contains(outStr, forbidden) {
			t.Errorf("output should not contain %q:\n%s", forbidden, outStr)
		}
	}
	for _, required := range []string{`"trackerPattern": "tracker_1"`, `"trackerDomains"`, `"tracker.xyz"`} {
		if !strings.Contains(outStr, required) {
			t.Errorf("output missing %q:\n%s", required, outStr)
		}
	}
}

// TestBuildRuleFilePreservesWildcardTrackers confirms that rules with
// trackerPattern="*" and trackerDomains=["*"] are exported verbatim —
// a wildcard is not personal data and consumers need to see it.
func TestBuildRuleFilePreservesWildcardTrackers(t *testing.T) {
	raw := json.RawMessage(`{
		"name": "All trackers",
		"trackerPattern": "*",
		"trackerDomains": ["*"],
		"conditions": {}
	}`)
	automation := Automation{ID: 1, Name: "All trackers", Raw: raw}

	out, err := buildRuleFile(automation, "all-trackers", &Config{}, 1)
	if err != nil {
		t.Fatalf("buildRuleFile: %v", err)
	}
	if !strings.Contains(string(out), `"trackerPattern": "*"`) {
		t.Errorf("wildcard trackerPattern lost:\n%s", out)
	}
	if !strings.Contains(string(out), `"*"`) {
		t.Errorf("wildcard trackerDomains lost:\n%s", out)
	}
}
