// fingerprint.go — content-derived stable identity for condition nodes.
//
// A Fingerprint is a hex-encoded SHA-256 truncated to 16 bytes (32 chars).
// Two condition nodes have the same fingerprint iff they represent the
// same logical test. See docs/qui-sync/customize-v2-design.md §4.1.

package customize

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Fingerprint is an opaque content-derived ID for a condition node.
// Stable across runs (deterministic hashing). Not stable across changes
// to the Compute algorithm — guard with Customization.SchemaVersion.
type Fingerprint string

// Compute returns the full fingerprint of a condition node.
//
// For a LEAF condition (has "field"): hash(field, operator, value, negate).
// For a GROUP condition (has "conditions"): hash(operator, sorted(children's fingerprints)).
//
// Returns empty Fingerprint("") if node is nil or not a recognisable
// condition shape. Callers treat empty fingerprint as "unmatched".
func Compute(node any) Fingerprint {
	m, ok := node.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	if isLeaf(m) {
		return leafFingerprint(m)
	}
	if isGroup(m) {
		return groupFingerprint(m)
	}
	return ""
}

// Partial returns a fingerprint of a leaf that ignores its `value`
// field. Two leaves with the same Partial fingerprint test the same
// thing (e.g. both are `TAGS EQUAL <something>`) but may have different
// concrete values.
//
// Used by the value-modification matcher (match.go Pass 2) to pair
// "same leaf, different value" cases — emit OpReplace instead of
// add+remove pair.
//
// Returns empty for non-leaf nodes (groups don't participate in Pass 2).
func Partial(node any) Fingerprint {
	m, ok := node.(map[string]any)
	if !ok || m == nil || !isLeaf(m) {
		return ""
	}
	// Hash key uses a "P:" prefix so a partial-fp can never collide with
	// a full-fp on the same leaf (defense against accidental cross-bucket
	// lookups).
	field, _ := m["field"].(string)
	op, _ := m["operator"].(string)
	negate := boolField(m, "negate")
	payload := fmt.Sprintf("P:leaf|field=%s|op=%s|negate=%t", field, op, negate)
	return hash16(payload)
}

// Skeleton returns a STRUCTURE-ONLY fingerprint that ignores every leaf
// `value` but preserves field+operator+negate and the full group nesting.
// Two nodes share a Skeleton iff they describe the same SHAPE of test,
// regardless of the concrete thresholds inside.
//
//	Leaf:  hash(field, operator, negate)             — value omitted
//	Group: hash(operator, sorted(child skeletons))   — recurses
//
// Apply uses this to relocate a group whose FULL fingerprint shifted only
// because the maintainer edited a value inside it (e.g. a torrent-age
// threshold). The skeleton is stable across such value-edits, so the
// user's descend op still finds its target and the maintainer's new value
// is preserved. Hashes carry an "S:" namespace so a skeleton can never
// collide with a full (Compute) or leaf-partial (Partial) fingerprint.
//
// Returns "" for nil / unrecognised nodes, same contract as Compute.
func Skeleton(node any) Fingerprint {
	m, ok := node.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	if isLeaf(m) {
		field, _ := m["field"].(string)
		op, _ := m["operator"].(string)
		negate := boolField(m, "negate")
		return hash16(fmt.Sprintf("S:leaf|field=%s|op=%s|negate=%t", field, op, negate))
	}
	if isGroup(m) {
		op, _ := m["operator"].(string)
		conds, _ := m["conditions"].([]any)
		childSks := make([]string, 0, len(conds))
		for _, c := range conds {
			sk := Skeleton(c)
			if sk == "" {
				sk = "?"
			}
			childSks = append(childSks, string(sk))
		}
		sort.Strings(childSks)
		return hash16(fmt.Sprintf("S:group|op=%s|children=%s", op, strings.Join(childSks, ",")))
	}
	return ""
}

// isLeaf reports whether a JSON-decoded condition node is a leaf
// (has a "field" key) rather than a group (has a "conditions" key).
//
// Defensive: returns false for nil or non-object inputs. Used by
// match.go to gate Pass 2 to leaves only.
func isLeaf(node any) bool {
	m, ok := node.(map[string]any)
	if !ok || m == nil {
		return false
	}
	_, hasField := m["field"]
	_, hasConditions := m["conditions"]
	return hasField && !hasConditions
}

// isGroup is the inverse of isLeaf for documentation; both are needed
// because a node could be neither (e.g. malformed input, schema drift).
func isGroup(node any) bool {
	m, ok := node.(map[string]any)
	if !ok || m == nil {
		return false
	}
	_, hasField := m["field"]
	_, hasConditions := m["conditions"]
	return hasConditions && !hasField
}

// ---- internal helpers ----

func leafFingerprint(m map[string]any) Fingerprint {
	field, _ := m["field"].(string)
	op, _ := m["operator"].(string)
	value := stringifyValue(m["value"])
	negate := boolField(m, "negate")
	payload := fmt.Sprintf("L:field=%s|op=%s|value=%s|negate=%t", field, op, value, negate)
	return hash16(payload)
}

func groupFingerprint(m map[string]any) Fingerprint {
	op, _ := m["operator"].(string)
	conds, _ := m["conditions"].([]any)
	childFps := make([]string, 0, len(conds))
	for _, c := range conds {
		fp := Compute(c)
		if fp == "" {
			// Unknown-shape child — fold a sentinel in so two malformed
			// children still produce distinct group fingerprints if their
			// positions differ relative to recognisable siblings. Cheap
			// defense against schema drift.
			fp = "?"
		}
		childFps = append(childFps, string(fp))
	}
	// Sort so that maintainer-reorderings of children don't shift the
	// group's identity. The semantic point of this matcher is that
	// order-in-source ≠ semantic-identity for AND/OR groupings.
	sort.Strings(childFps)
	payload := fmt.Sprintf("G:op=%s|children=%s", op, strings.Join(childFps, ","))
	return hash16(payload)
}

// stringifyValue converts a leaf's `value` (which may be string, number,
// bool, or null in upstream JSON) to a stable string form for hashing.
// Numbers serialised by Go's default formatting keep enough precision
// for Qui's domain (durations in seconds, byte counts, version strings).
func stringifyValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return fmt.Sprintf("%t", x)
	case float64:
		// JSON numbers decode as float64 in Go. Preserve integer-shape
		// when possible — `1836000.0` and `1836000` should hash equal.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func boolField(m map[string]any, key string) bool {
	v, ok := m[key].(bool)
	return ok && v
}

func hash16(payload string) Fingerprint {
	sum := sha256.Sum256([]byte(payload))
	return Fingerprint(hex.EncodeToString(sum[:16]))
}
