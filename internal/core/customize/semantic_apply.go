// semantic_apply.go — apply algorithm for v2 customize.
//
// SemanticApply takes an upstream rule + a list of ops (produced by
// SemanticDiff at capture time) and returns the resulting tree plus
// any conflicts that arose. See docs/qui-sync/customize-v2-design.md §5.3.
//
// Distinguishing op variants (no separate types, derived from fields):
//
//   Object-key op:    Match == nil  AND  Position == nil
//                     op.Path = parent path, op.Field = key
//
//   Array-element op: Match != nil  OR   Position != nil
//                     op.Path = path of the containing array
//
// Mutation model: every op walks to the OWNING object (parent map) of
// the value it touches, gets the value (object, array, or scalar),
// mutates locally, and writes back to the map. Slices are passed by
// value in Go, so we cannot mutate a slice via the parent's reference
// alone — we always write back through the map.

package customize

import (
	"encoding/json"
	"fmt"
)

// SemanticApply walks `upstream`, applies each op in order, and
// returns (newTree, conflicts). The input is never mutated — every
// mutation happens on a deep copy.
//
// Round-trip property: for any (upstream, live) pair without external
// drift, SemanticApply(upstream, SemanticDiff(upstream, live)) returns
// a tree deep-equal to live with zero conflicts.
func SemanticApply(upstream any, ops []Op) (any, []OpConflict) {
	result := deepCopy(upstream)
	var conflicts []OpConflict
	for i := range ops {
		applyOp(result, &ops[i], &conflicts)
	}
	return result, conflicts
}

// applyOp dispatches to the right handler based on whether this is an
// object-key op or an array-element op.
func applyOp(root any, op *Op, conflicts *[]OpConflict) {
	if isObjectKeyOp(op) {
		applyObjectKeyOp(root, op, conflicts)
		return
	}
	applyArrayOp(root, op, conflicts)
}

func isObjectKeyOp(op *Op) bool {
	return op.Match == nil && op.Position == nil
}

// applyObjectKeyOp handles Add/Remove/Replace on a map field. The
// parent map is at op.Path; the affected key is op.Field.
func applyObjectKeyOp(root any, op *Op, conflicts *[]OpConflict) {
	parent, ok := walkObjectPath(root, op.Path)
	if !ok {
		emit(conflicts, op, ConflictPathGone, SeverityHigh,
			fmt.Sprintf("Path %v no longer reachable", op.Path), nil)
		return
	}
	parentMap, ok := parent.(map[string]any)
	if !ok {
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			fmt.Sprintf("Object-key op expected map at %v, got %T", op.Path, parent), nil)
		return
	}
	if op.Field == "" {
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			"Object-key op missing Field", nil)
		return
	}

	switch op.Kind {
	case OpAdd:
		// Defensive: if the key already exists (e.g. maintainer also
		// added the same key with a different value), treat as
		// value_drifted so the UI can ask. Most paths just set.
		if existing, present := parentMap[op.Field]; present && !deepEqual(existing, op.Value) {
			emit(conflicts, op, ConflictValueDrifted, SeverityMedium,
				fmt.Sprintf("Key %q already exists with different value; maintainer added it too", op.Field),
				existing)
			return
		}
		parentMap[op.Field] = deepCopy(op.Value)

	case OpRemove:
		if _, present := parentMap[op.Field]; !present {
			emit(conflicts, op, ConflictTargetGone, SeverityLow,
				fmt.Sprintf("Key %q already absent", op.Field), nil)
			return
		}
		delete(parentMap, op.Field)

	case OpReplace:
		existing, present := parentMap[op.Field]
		if !present {
			emit(conflicts, op, ConflictTargetGone, SeverityMedium,
				fmt.Sprintf("Key %q removed by maintainer; cannot replace", op.Field), nil)
			return
		}
		// B6 fix from code review: implicit compare-and-swap. If
		// the captured op carries OldValue and upstream-now has
		// drifted away from it, the maintainer has edited the same
		// field — surface value_drifted instead of silently winning.
		// Ops captured before this fix don't carry OldValue; we
		// preserve the legacy "always overwrite" behaviour for them
		// rather than blocking, so a v0.6→v0.6.1 in-place upgrade
		// doesn't reject in-flight customizations. New captures
		// always set OldValue (semantic_diff.go does it
		// unconditionally for object-key Replaces).
		if op.OldValue != nil && !deepEqual(existing, op.OldValue) {
			emit(conflicts, op, ConflictValueDrifted, SeverityMedium,
				fmt.Sprintf("You and the maintainer both changed key %q. Yours: %v, theirs: %v",
					op.Field, op.Value, existing),
				existing)
			return
		}
		parentMap[op.Field] = deepCopy(op.Value)

	default:
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			fmt.Sprintf("Unknown op kind for object-key op: %q", op.Kind), nil)
	}
}

// applyArrayOp handles Add/Remove/Replace/Descend on an array element.
// op.Path leads to the array; mutation goes via the owning map.
func applyArrayOp(root any, op *Op, conflicts *[]OpConflict) {
	if len(op.Path) == 0 {
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			"Array op with empty Path — root is not an array in Qui rules", nil)
		return
	}
	owner, key, ok := walkObjectParent(root, op.Path)
	if !ok {
		emit(conflicts, op, ConflictPathGone, SeverityHigh,
			fmt.Sprintf("Path %v no longer reachable", op.Path), nil)
		return
	}
	rawArr, present := owner[key]
	if !present {
		emit(conflicts, op, ConflictPathGone, SeverityHigh,
			fmt.Sprintf("Array %q removed by maintainer at %v", key, op.Path), nil)
		return
	}
	arr, ok := rawArr.([]any)
	if !ok {
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			fmt.Sprintf("Expected array at %v, got %T", op.Path, rawArr), nil)
		return
	}

	switch op.Kind {
	case OpAdd:
		idx := resolvePosition(arr, op.Position, op, conflicts)
		arr = insertAt(arr, idx, deepCopy(op.Value))
		owner[key] = arr

	case OpRemove:
		idx, n := findByFingerprint(arr, op.Match.Fingerprint)
		if n == 0 {
			emit(conflicts, op, ConflictTargetGone, SeverityLow,
				fmt.Sprintf("Element with fingerprint %s already gone", op.Match.Fingerprint), nil)
			return
		}
		if n > 1 {
			emit(conflicts, op, ConflictAmbiguous, SeverityInfo,
				fmt.Sprintf("%d duplicates of fingerprint %s; removing first", n, op.Match.Fingerprint), nil)
		}
		arr = removeAt(arr, idx)
		owner[key] = arr

	case OpReplace:
		// Implicit compare-and-swap via Match.Fingerprint (OLD upstream fp).
		idx, n := findByFingerprint(arr, op.Match.Fingerprint)
		if n == 0 {
			// B3 fix: precise drift-detection via Match.Partial,
			// captured alongside the full fp. Partial = same field+
			// operator+negate, any value. A hit here means the
			// maintainer edited the same leaf-test but with a
			// different value → value_drifted. (Pre-B3 fallback used
			// a probe-by-Field heuristic that false-matched across
			// operators.)
			if op.Match.Partial != "" {
				partialIdx := findByPartialFingerprint(arr, op.Match.Partial)
				if partialIdx >= 0 {
					m, _ := arr[partialIdx].(map[string]any)
					their := ""
					if m != nil {
						their = fmt.Sprintf("%v", m[op.Field])
					}
					emit(conflicts, op, ConflictValueDrifted, SeverityMedium,
						fmt.Sprintf("You and the maintainer both changed field %q. Yours: %v, theirs: %s", op.Field, op.Value, their),
						m)
					return
				}
			}
			emit(conflicts, op, ConflictTargetGone, SeverityMedium,
				fmt.Sprintf("Element with fingerprint %s removed by maintainer; cannot replace field %q",
					op.Match.Fingerprint, op.Field), nil)
			return
		}
		if n > 1 {
			emit(conflicts, op, ConflictAmbiguous, SeverityInfo,
				fmt.Sprintf("%d duplicates of fingerprint %s; replacing first", n, op.Match.Fingerprint), nil)
		}
		leaf, ok := arr[idx].(map[string]any)
		if !ok {
			emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
				fmt.Sprintf("Element at %v[%d] is not an object; cannot field-replace", op.Path, idx), nil)
			return
		}
		leaf[op.Field] = deepCopy(op.Value)
		// leaf is map → reference type; mutation visible without writeback.

	case OpDescend:
		idx, n := findByFingerprint(arr, op.Match.Fingerprint)
		if n > 1 {
			// Exact-fp duplicates are byte-identical groups, so descending
			// into either is equivalent — info only.
			emit(conflicts, op, ConflictAmbiguous, SeverityInfo,
				fmt.Sprintf("%d duplicates of group fingerprint %s; descending into first", n, op.Match.Fingerprint), nil)
		}
		if n == 0 {
			// Full fp missed: the maintainer changed something inside, or
			// removed, the captured group. Relocate by structure-only
			// skeleton, disambiguating same-skeleton siblings by how many
			// children they still share with the group we captured. A
			// value-edited copy keeps most children and wins; a true tie
			// blocks rather than risk applying our edit to the wrong group.
			target, decision := chooseDescendTarget(arr, op.Match.Skeleton, op.Match.Children)
			switch decision {
			case "none":
				emit(conflicts, op, ConflictTargetGone, SeverityMedium,
					fmt.Sprintf("Group with fingerprint %s removed by maintainer; cannot descend", op.Match.Fingerprint), nil)
				return
			case "ambiguous":
				emit(conflicts, op, ConflictAmbiguous, SeverityMedium,
					fmt.Sprintf("Several groups share structure %s and the exact one you customised is gone; cannot safely pick which to update",
						op.Match.Skeleton), nil)
				return
			}
			idx = target
		}
		// Apply sub-ops with arr[idx] as the new root. Sub-paths are
		// relative to the matched group (per design Q6).
		for j := range op.SubOps {
			applyOp(arr[idx], &op.SubOps[j], conflicts)
		}

	default:
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			fmt.Sprintf("Unknown op kind for array op: %q", op.Kind), nil)
	}
}

// resolvePosition turns a Position hint into a concrete array index.
// If the anchor is gone, falls back to end (or start for "before") and
// reports an anchor_gone conflict at low severity.
func resolvePosition(arr []any, pos *Position, op *Op, conflicts *[]OpConflict) int {
	if pos == nil {
		// Defensive — capture always emits a Position for array Adds.
		return len(arr)
	}
	switch pos.Kind {
	case "start":
		return 0
	case "end":
		return len(arr)
	case "after":
		idx, n := findByFingerprint(arr, pos.Anchor)
		if n == 0 {
			emit(conflicts, op, ConflictAnchorGone, SeverityLow,
				fmt.Sprintf("Insert-after anchor %s gone; appended at end", pos.Anchor), nil)
			return len(arr)
		}
		return idx + 1
	case "before":
		idx, n := findByFingerprint(arr, pos.Anchor)
		if n == 0 {
			emit(conflicts, op, ConflictAnchorGone, SeverityLow,
				fmt.Sprintf("Insert-before anchor %s gone; prepended at start", pos.Anchor), nil)
			return 0
		}
		return idx
	default:
		emit(conflicts, op, ConflictTypeChanged, SeverityHigh,
			fmt.Sprintf("Unknown Position.Kind: %q", pos.Kind), nil)
		return len(arr)
	}
}

// ---- helpers ----

func walkObjectPath(root any, path []string) (any, bool) {
	cur := root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, exists := m[key]
		if !exists {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// walkObjectParent walks all but the last path segment, returning
// (parentMap, lastKey, ok). For empty path it returns (nil, "", false)
// — the root has no "parent" we can write to.
func walkObjectParent(root any, path []string) (map[string]any, string, bool) {
	if len(path) == 0 {
		return nil, "", false
	}
	parentPath := path[:len(path)-1]
	lastKey := path[len(path)-1]
	v, ok := walkObjectPath(root, parentPath)
	if !ok {
		return nil, "", false
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, "", false
	}
	return m, lastKey, true
}

func insertAt(arr []any, idx int, value any) []any {
	if idx < 0 {
		idx = 0
	}
	if idx > len(arr) {
		idx = len(arr)
	}
	out := make([]any, 0, len(arr)+1)
	out = append(out, arr[:idx]...)
	out = append(out, value)
	out = append(out, arr[idx:]...)
	return out
}

func removeAt(arr []any, idx int) []any {
	if idx < 0 || idx >= len(arr) {
		return arr
	}
	out := make([]any, 0, len(arr)-1)
	out = append(out, arr[:idx]...)
	out = append(out, arr[idx+1:]...)
	return out
}

// deepCopy clones a JSON-decoded value via marshal+unmarshal. Coarse
// but safe — preserves the standard set of types (object/array/string/
// number/bool/null). Avoids reflect.DeepCopy gymnastics.
func deepCopy(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v // fall back to shared reference; tests will detect
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// deepEqual compares two JSON-decoded values structurally. Wrapper
// around encoding/json round-trip equality — handles type coercion
// the same way deepCopy does, so a copied-then-compared value matches.
func deepEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func emit(out *[]OpConflict, op *Op, reason ConflictReason, sev ConflictSeverity, msg string, suggested any) {
	*out = append(*out, OpConflict{
		Op:             *op,
		Reason:         reason,
		Severity:       sev,
		Message:        msg,
		SuggestedValue: suggested,
	})
}
