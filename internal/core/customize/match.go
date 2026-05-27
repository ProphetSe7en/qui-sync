// match.go — pair-up algorithm between two condition arrays.
//
// The two-pass matcher (design §5.1) is the heart of the semantic-diff
// model: it pairs elements by content fingerprint, not by array index,
// so maintainer reorderings don't show up as diffs.
//
// Pass 1: full-fingerprint identity match. Equal content → paired.
// Pass 2: leaf partial-fingerprint match among the still-unmatched.
//         Same (field, operator, negate), different value → paired
//         (these become OpReplace at capture).
// Pass 3: anything still unmatched is a genuine add or remove.

package customize

import "sort"

// Pair records that upstreamArr[UpIdx] corresponds to liveArr[LvIdx].
// Used by both capture (to emit Descend / Replace for paired elements)
// and apply (to know which elements to look up by fingerprint).
type Pair struct {
	UpIdx int
	LvIdx int
}

// MatchResult is the classified output of matchArrays.
//
// IdentityPairs:   full-fingerprint matches. Element content is equal.
//                  Capture emits OpDescend if the matched elements are
//                  groups whose descendants differ; nothing if they're
//                  byte-identical leaves.
//
// ModifyPairs:     leaf partial-fingerprint matches. Same key, different
//                  value. Capture emits one OpReplace per differing field.
//
// AddedInLive:     liveArr indices with no upstream counterpart in either
//                  pass. Capture emits OpAdd with a Position derived from
//                  surrounding matched elements.
//
// RemovedFromLive: upstreamArr indices with no live counterpart in either
//                  pass. Capture emits OpRemove.
type MatchResult struct {
	IdentityPairs   []Pair
	ModifyPairs     []Pair
	AddedInLive     []int
	RemovedFromLive []int
}

// matchArrays runs the two-pass pair-up algorithm. Inputs are JSON-decoded
// arrays of condition objects (each a `map[string]any` once unmarshalled).
//
// Property: every index in upstreamArr appears in exactly one of
// IdentityPairs.UpIdx, ModifyPairs.UpIdx, RemovedFromLive — never more
// than one. Same for liveArr indices vs LvIdx / AddedInLive.
func matchArrays(upstreamArr, liveArr []any) MatchResult {
	// ---- Pass 1: full-fingerprint identity match ----
	upBuckets := bucketBy(upstreamArr, Compute)
	lvBuckets := bucketBy(liveArr, Compute)

	identityPairs := pairBuckets(upBuckets, lvBuckets)

	pairedUp := indexSet(identityPairs, true)
	pairedLv := indexSet(identityPairs, false)

	upUnmatched := unmatched(len(upstreamArr), pairedUp)
	lvUnmatched := unmatched(len(liveArr), pairedLv)

	// ---- Pass 2: value-modification on leaves (partial fingerprint) ----
	// Bucket only LEAVES among the unmatched; groups stay out of Pass 2
	// because group identity is structural — a "modified group" surfaces
	// via recursive descent on its container, not via this pass.
	upPartial := bucketSubset(upstreamArr, upUnmatched, partialIfLeaf)
	lvPartial := bucketSubset(liveArr, lvUnmatched, partialIfLeaf)

	modifyPairs := pairBuckets(upPartial, lvPartial)

	pairedUp2 := indexSet(modifyPairs, true)
	pairedLv2 := indexSet(modifyPairs, false)

	// ---- Final classification ----
	var removed []int
	for _, i := range upUnmatched {
		if !pairedUp2[i] {
			removed = append(removed, i)
		}
	}
	var added []int
	for _, i := range lvUnmatched {
		if !pairedLv2[i] {
			added = append(added, i)
		}
	}

	return MatchResult{
		IdentityPairs:   identityPairs,
		ModifyPairs:     modifyPairs,
		AddedInLive:     added,
		RemovedFromLive: removed,
	}
}

// findByFingerprint scans an array for elements whose full fingerprint
// equals fp. Returns (firstIdx, count).
//
//   count == 0 → not found
//   count == 1 → unambiguous match at firstIdx
//   count > 1  → duplicate fingerprints; apply uses firstIdx with an
//                ambiguous_target conflict logged at info severity.
//
// Used at apply time by every op kind except OpAdd-with-Position-start/end.
func findByFingerprint(arr []any, fp Fingerprint) (firstIdx, count int) {
	firstIdx = -1
	for i, e := range arr {
		if Compute(e) == fp {
			if count == 0 {
				firstIdx = i
			}
			count++
		}
	}
	return firstIdx, count
}

// findByPartialFingerprint scans an array for the first leaf whose
// partial fingerprint (Partial(node)) equals pfp.
//
// Used by SemanticApply to construct richer value_drifted conflict
// messages: when an OpReplace's full-fingerprint match misses, this
// helper checks whether a leaf with the same key (different value)
// exists — if so, the maintainer changed the same field we wanted to
// change, and we surface a value_drifted conflict instead of the
// generic target_gone.
//
// Returns -1 if no match.
func findByPartialFingerprint(arr []any, pfp Fingerprint) int {
	for i, e := range arr {
		if Partial(e) == pfp {
			return i
		}
	}
	return -1
}

// ---- internal helpers ----

// bucketBy groups array indices by a fingerprint derived from each
// element. Empty fingerprints (e.g. for malformed nodes) are dropped —
// they can never participate in a deterministic match.
func bucketBy(arr []any, fpFunc func(any) Fingerprint) map[Fingerprint][]int {
	out := map[Fingerprint][]int{}
	for i, e := range arr {
		fp := fpFunc(e)
		if fp == "" {
			continue
		}
		out[fp] = append(out[fp], i)
	}
	return out
}

// bucketSubset is bucketBy but restricted to a known subset of indices,
// AND the fingerprint-function may return "" to signal "not in scope"
// (e.g. partialIfLeaf returns "" for groups so they don't bucket).
func bucketSubset(arr []any, indices []int, fpFunc func(any) Fingerprint) map[Fingerprint][]int {
	out := map[Fingerprint][]int{}
	for _, i := range indices {
		fp := fpFunc(arr[i])
		if fp == "" {
			continue
		}
		out[fp] = append(out[fp], i)
	}
	return out
}

// pairBuckets pairs the smaller of (upstream count, live count) for
// each fingerprint, in index-order. Returns the resulting pairs
// SORTED by UpIdx ascending — Go map iteration is non-deterministic,
// so without this sort `diffArray` would emit Descend/Replace ops in
// random order across runs and produce spurious on-disk JSON churn
// (C2 in code review).
func pairBuckets(up, lv map[Fingerprint][]int) []Pair {
	var pairs []Pair
	for fp, upIdxs := range up {
		lvIdxs, ok := lv[fp]
		if !ok {
			continue
		}
		n := len(upIdxs)
		if len(lvIdxs) < n {
			n = len(lvIdxs)
		}
		for k := 0; k < n; k++ {
			pairs = append(pairs, Pair{UpIdx: upIdxs[k], LvIdx: lvIdxs[k]})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].UpIdx < pairs[j].UpIdx })
	return pairs
}

// indexSet collects either UpIdx (side=true) or LvIdx (side=false)
// from a set of pairs into a lookup map.
func indexSet(pairs []Pair, upstream bool) map[int]bool {
	out := map[int]bool{}
	for _, p := range pairs {
		if upstream {
			out[p.UpIdx] = true
		} else {
			out[p.LvIdx] = true
		}
	}
	return out
}

// unmatched returns the indices in [0..n) not present in the given set.
// Result is sorted ascending (we iterate i in order).
func unmatched(n int, paired map[int]bool) []int {
	var out []int
	for i := 0; i < n; i++ {
		if !paired[i] {
			out = append(out, i)
		}
	}
	return out
}

// partialIfLeaf returns Partial(node) for leaves, "" for everything
// else. Used as the fingerprint-function for Pass 2's leaf-only bucket.
func partialIfLeaf(node any) Fingerprint {
	if !isLeaf(node) {
		return ""
	}
	return Partial(node)
}
