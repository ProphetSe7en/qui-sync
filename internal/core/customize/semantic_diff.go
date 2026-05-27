// semantic_diff.go — capture algorithm for v2 customize.
//
// SemanticDiff is the v0.6+ replacement for the RFC 6902 JSON Patch
// generation in capture.go. Output ops match by content fingerprint
// instead of array index, so maintainer reorderings don't leak into
// user customize-state. See docs/qui-sync/customize-v2-design.md §5.2.
//
// Phase C: implementation. Phase D adds SemanticApply. Phase E wires
// them into handlers. Phase F migrates v0.5 customizations. v0.5 code
// stays parallel until Phase E flips the switch.

package customize

import (
	"reflect"
)

// SemanticDiff walks two JSON-decoded values (upstream + live) and
// produces an ordered list of Op describing how to transform upstream
// into live.
//
// Both inputs are expected to be `map[string]any` (an object) for the
// top-level Qui rule; nested values can be objects, arrays of objects,
// or scalars. Type mismatches at the same path emit an object-key
// Replace (not a structural failure — apply blocks on it via
// type_changed conflict).
func SemanticDiff(upstream, live any) []Op {
	var ops []Op
	diffNode(upstream, live, nil, &ops)
	return ops
}

// diffNode is the recursive workhorse. `path` is the chain of object
// keys (not array indices) leading to the current pair of values. When
// it descends into an array, `diffArray` takes over and emits ops
// with that path; array elements are matched by fingerprint, not by
// position within the path.
func diffNode(up, lv any, path []string, out *[]Op) {
	// Both nil → no change. (Caller passes through.)
	if up == nil && lv == nil {
		return
	}

	upMap, upIsMap := up.(map[string]any)
	lvMap, lvIsMap := lv.(map[string]any)

	// Same object shape on both sides — recurse into keys.
	if upIsMap && lvIsMap {
		diffObject(upMap, lvMap, path, out)
		return
	}

	// Both arrays at this node — uncommon at top level (Qui rule root
	// is always an object), but supported defensively.
	upArr, upIsArr := up.([]any)
	lvArr, lvIsArr := lv.([]any)
	if upIsArr && lvIsArr {
		diffArray(upArr, lvArr, path, out)
		return
	}

	// Scalars (string/number/bool/null) or shape mismatch.
	// Equal → no op. Different → no useful Op at this leaf-level (the
	// parent diffObject emits a field-level Replace via its else-branch
	// before reaching here, so this is only hit when path == nil i.e.
	// caller passed two scalars directly to SemanticDiff — which is
	// degenerate but covered for safety).
	if !reflect.DeepEqual(up, lv) && len(path) == 0 {
		*out = append(*out, Op{
			Kind:  OpReplace,
			Path:  []string{},
			Field: "",
			Value: lv,
		})
	}
}

// diffObject diffs two map[string]any. Keys that exist in only one
// side become object-key Add/Remove ops; keys present on both with
// differing values either recurse (object/array) or emit object-key
// Replace (scalar).
func diffObject(upMap, lvMap map[string]any, path []string, out *[]Op) {
	keys := keyUnion(upMap, lvMap)
	for _, key := range keys {
		upVal, upHas := upMap[key]
		lvVal, lvHas := lvMap[key]

		// Object-key Remove. Path = parent path, Field = key. Apply
		// detects "object-key op" by Match==nil + Position==nil.
		if upHas && !lvHas {
			*out = append(*out, Op{
				Kind:  OpRemove,
				Path:  copyPath(path),
				Match: nil,
				Field: key,
			})
			continue
		}

		// Object-key Add. Same convention.
		if !upHas && lvHas {
			*out = append(*out, Op{
				Kind:  OpAdd,
				Path:  copyPath(path),
				Field: key,
				Value: lvVal,
				// Position is nil — that's how apply disambiguates
				// object-key Add from array-element Add.
			})
			continue
		}

		// Both sides have the key. Compare values.
		if reflect.DeepEqual(upVal, lvVal) {
			continue
		}

		// Both arrays → array-diff at child path.
		if upArr, ok1 := upVal.([]any); ok1 {
			if lvArr, ok2 := lvVal.([]any); ok2 {
				diffArray(upArr, lvArr, appendPath(path, key), out)
				continue
			}
		}

		// Both objects → recurse.
		if upObj, ok1 := upVal.(map[string]any); ok1 {
			if lvObj, ok2 := lvVal.(map[string]any); ok2 {
				diffObject(upObj, lvObj, appendPath(path, key), out)
				continue
			}
		}

		// Scalar mismatch (or type change object↔array). Emit
		// object-key Replace at the parent's path; apply sees this
		// when Match==nil and uses Field as the key to overwrite.
		// OldValue carries the upstream-at-capture-time value so
		// apply can do an implicit compare-and-swap (B6 fix from
		// review) — if upstream-now has a different value, we
		// surface value_drifted instead of silently winning.
		*out = append(*out, Op{
			Kind:     OpReplace,
			Path:     copyPath(path),
			Match:    nil,
			Field:    key,
			Value:    lvVal,
			OldValue: upVal,
		})
	}
}

// diffArray emits semantic-diff ops for two arrays at the given path.
// Uses matchArrays from match.go to classify pairs, then dispatches
// per category. Output order: Descend / Replace first (most contained
// changes), then Remove, then Add (so derivePosition's anchor lookups
// see a stable picture).
func diffArray(upArr, lvArr []any, path []string, out *[]Op) {
	r := matchArrays(upArr, lvArr)

	// Identity pairs: only groups can need recursion (leaves matched
	// on full fingerprint are byte-equal by definition).
	for _, p := range r.IdentityPairs {
		up := upArr[p.UpIdx]
		lv := lvArr[p.LvIdx]
		if !isGroup(up) || !isGroup(lv) {
			continue
		}
		// Recurse into the group. SubOps use paths relative to the
		// matched group's interior (per design Q6 — relative paths
		// keep ops portable across upstream restructures that move
		// a whole group elsewhere).
		var subOut []Op
		diffNode(up, lv, nil, &subOut)
		if len(subOut) == 0 {
			continue
		}
		*out = append(*out, Op{
			Kind:   OpDescend,
			Path:   path,
			Match:  &MatchSpec{Fingerprint: Compute(up)},
			SubOps: subOut,
		})
	}

	// Modification pairs (Pass 2): leaf-only. Same partial fp, different
	// concrete value (and/or negate). Emit one field-level Replace per
	// differing field. Match.Fingerprint is the OLD (upstream) full
	// fingerprint — apply uses it as implicit compare-and-swap.
	for _, p := range r.ModifyPairs {
		upLeaf, _ := upArr[p.UpIdx].(map[string]any)
		lvLeaf, _ := lvArr[p.LvIdx].(map[string]any)
		oldFp := Compute(upLeaf)
		// B3 fix: also stash the partial fp so apply can do an exact
		// lookup (instead of probing by field-existence) when the
		// full fp misses because the maintainer drifted the same
		// field. Partial fp = field+operator+negate, same key any value.
		partialFp := Partial(upLeaf)

		for _, field := range keyUnion(upLeaf, lvLeaf) {
			// Same value → skip. Different (or one-sided) → emit Replace.
			if reflect.DeepEqual(upLeaf[field], lvLeaf[field]) {
				continue
			}
			*out = append(*out, Op{
				Kind:  OpReplace,
				Path:  copyPath(path),
				Match: &MatchSpec{Fingerprint: oldFp, Partial: partialFp},
				Field: field,
				Value: lvLeaf[field],
			})
		}
	}

	// Removes (upstream-only after both passes).
	for _, upIdx := range r.RemovedFromLive {
		elem := upArr[upIdx]
		*out = append(*out, Op{
			Kind:  OpRemove,
			Path:  path,
			Match: &MatchSpec{Fingerprint: Compute(elem)},
		})
	}

	// Adds (live-only after both passes). Position is derived from
	// surrounding matched elements so apply can re-insert at the right
	// place even if upstream reorders around it.
	for _, lvIdx := range r.AddedInLive {
		elem := lvArr[lvIdx]
		pos := derivePosition(lvArr, lvIdx, r)
		*out = append(*out, Op{
			Kind:     OpAdd,
			Path:     path,
			Position: pos,
			Value:    elem,
		})
	}
}

// derivePosition picks a Position hint for an added element.
//   - lvIdx == 0           → Position{Kind: "start"}
//   - lvIdx == last         → Position{Kind: "end"}
//   - otherwise             → Position{Kind: "after", Anchor: fp(lvArr[lvIdx-1])}
//
// We deliberately anchor on the IMMEDIATE predecessor regardless of
// whether it's a paired (already-in-upstream) element or an earlier
// added element from the same diff. Ops are applied in order, so when
// the predecessor is a sibling Add, that Add has already run by the
// time we reach this one and its element has the same fingerprint
// upstream-now as Compute(lvArr[lvIdx-1]) gave us at capture time.
//
// This fixes B1 (multi-add at the same anchor) from the Phase D code
// review: previously two adjacent adds both resolved to Position.start,
// then apply did `insertAt(0, A)` followed by `insertAt(0, B)` and
// produced `[B, A, ...]` instead of `[A, B, ...]`. Anchoring each add
// on its true predecessor makes ops independent and order-preserving.
//
// FallbackEnd is always true at capture time (per design §4.2): if
// the anchor goes missing at apply (maintainer-side change between
// capture and apply), the element lands at end-of-array with a
// low-severity anchor_gone conflict rather than failing the op.
func derivePosition(lvArr []any, lvIdx int, _ MatchResult) *Position {
	if lvIdx == 0 {
		return &Position{Kind: "start", FallbackEnd: true}
	}
	if lvIdx == len(lvArr)-1 {
		return &Position{Kind: "end", FallbackEnd: true}
	}
	return &Position{
		Kind:        "after",
		Anchor:      Compute(lvArr[lvIdx-1]),
		FallbackEnd: true,
	}
}

// keyUnion returns the deterministic union of two maps' keys. Order
// is stable for testability (sorted ascending) — diff output ordering
// for object-key Add/Remove/Replace then becomes deterministic for
// snapshot tests.
func keyUnion(a, b map[string]any) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	// Sort for determinism.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// appendPath returns a NEW slice (never mutates the caller's). Important
// because recursive descent shares the slice header across siblings —
// without a copy, one branch's append would clobber another's path view.
func appendPath(base []string, key string) []string {
	out := make([]string, len(base)+1)
	copy(out, base)
	out[len(base)] = key
	return out
}

// copyPath returns a fresh copy of `base`. Same defensive purpose as
// appendPath — but no new key appended.
func copyPath(base []string) []string {
	out := make([]string, len(base))
	copy(out, base)
	return out
}
