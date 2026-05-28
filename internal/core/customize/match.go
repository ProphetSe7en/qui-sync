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
// GroupPairs:      structural matches between GROUPS whose interior
//                  changed (so their full fingerprints differ). Paired by
//                  best child-fingerprint overlap, not by index. Capture
//                  emits OpDescend and recurses; without this pass an
//                  edited nested group would surface as a wholesale
//                  remove+add and swallow every maintainer edit inside it.
//
// RemovedFromLive: upstreamArr indices with no live counterpart in any
//                  pass. Capture emits OpRemove.
type MatchResult struct {
	IdentityPairs   []Pair
	ModifyPairs     []Pair
	GroupPairs      []Pair
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

	// ---- Pass 2.5: structural group pairing ----
	// A group whose interior changed has a different full fingerprint
	// (Pass 1 misses) and is not a leaf (Pass 2 skips it). Pair such
	// groups by how many child fingerprints they still share, so capture
	// can recurse into them instead of treating the whole group as
	// replaced. Operates only on what's still unmatched after Passes 1-2.
	var upStillUn, lvStillUn []int
	for _, i := range upUnmatched {
		if !pairedUp2[i] {
			upStillUn = append(upStillUn, i)
		}
	}
	for _, i := range lvUnmatched {
		if !pairedLv2[i] {
			lvStillUn = append(lvStillUn, i)
		}
	}
	groupPairs := pairGroupsByOverlap(upstreamArr, liveArr, upStillUn, lvStillUn)
	pairedUp3 := indexSet(groupPairs, true)
	pairedLv3 := indexSet(groupPairs, false)

	// ---- Final classification ----
	var removed []int
	for _, i := range upStillUn {
		if !pairedUp3[i] {
			removed = append(removed, i)
		}
	}
	var added []int
	for _, i := range lvStillUn {
		if !pairedLv3[i] {
			added = append(added, i)
		}
	}

	return MatchResult{
		IdentityPairs:   identityPairs,
		ModifyPairs:     modifyPairs,
		GroupPairs:      groupPairs,
		AddedInLive:     added,
		RemovedFromLive: removed,
	}
}

// pairGroupsByOverlap pairs still-unmatched GROUPS across the two arrays
// by child-fingerprint overlap. Two groups are candidates only if they
// share the same `operator` AND at least one child full-fingerprint.
// Among candidates, greedy highest-overlap-first wins, each index used
// once. Deterministic: ties break by (upIdx, lvIdx), result sorted by UpIdx.
//
// Overlap (not skeleton-equality) is the right capture-time signal because
// the user may have added/removed/edited children — the groups are "the
// same group, edited," and they keep most of their children in common.
func pairGroupsByOverlap(upArr, lvArr []any, upIdxs, lvIdxs []int) []Pair {
	type groupInfo struct {
		op       string
		children map[Fingerprint]int
	}
	collect := func(arr []any, idxs []int) map[int]groupInfo {
		out := map[int]groupInfo{}
		for _, i := range idxs {
			m, ok := arr[i].(map[string]any)
			if !ok || !isGroup(m) {
				continue
			}
			op, _ := m["operator"].(string)
			out[i] = groupInfo{op: op, children: childFingerprintCounts(m)}
		}
		return out
	}
	upInfo := collect(upArr, upIdxs)
	lvInfo := collect(lvArr, lvIdxs)

	type cand struct {
		up, lv, score int
	}
	var cands []cand
	for ui, ug := range upInfo {
		for li, lg := range lvInfo {
			if ug.op != lg.op {
				continue
			}
			if s := overlapScore(ug.children, lg.children); s > 0 {
				cands = append(cands, cand{up: ui, lv: li, score: s})
			}
		}
	}
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].score != cands[b].score {
			return cands[a].score > cands[b].score
		}
		if cands[a].up != cands[b].up {
			return cands[a].up < cands[b].up
		}
		return cands[a].lv < cands[b].lv
	})

	usedUp := map[int]bool{}
	usedLv := map[int]bool{}
	var pairs []Pair
	for _, c := range cands {
		if usedUp[c.up] || usedLv[c.lv] {
			continue
		}
		usedUp[c.up] = true
		usedLv[c.lv] = true
		pairs = append(pairs, Pair{UpIdx: c.up, LvIdx: c.lv})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].UpIdx < pairs[j].UpIdx })
	return pairs
}

// childFingerprintCounts returns a multiset of the full fingerprints of a
// group's immediate children. Used to score group-to-group overlap.
func childFingerprintCounts(group map[string]any) map[Fingerprint]int {
	out := map[Fingerprint]int{}
	conds, _ := group["conditions"].([]any)
	for _, c := range conds {
		fp := Compute(c)
		if fp == "" {
			continue
		}
		out[fp]++
	}
	return out
}

// overlapScore is the size of the multiset intersection of two child-fp
// maps: sum over shared fingerprints of min(countA, countB).
func overlapScore(a, b map[Fingerprint]int) int {
	score := 0
	for fp, na := range a {
		if nb, ok := b[fp]; ok {
			if nb < na {
				score += nb
			} else {
				score += na
			}
		}
	}
	return score
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

// chooseDescendTarget selects the array index to descend into when an
// OpDescend's exact full-fingerprint missed (the maintainer changed
// something inside, or removed, the captured group). It scans for
// elements sharing the captured group's structure-only Skeleton, then
// among those picks the one sharing the most immediate children (full
// fingerprints) with the captured group:
//
//	"none"      — no structural twin survives; caller emits target_gone.
//	"unique"    — a single best-overlap twin; idx is it, descend there.
//	"ambiguous" — two+ twins tie on best overlap; caller blocks rather
//	              than guess which one the user actually customised.
//
// A value-edited copy of the original group keeps most of its children
// (only the edited leaves' fingerprints shift), so it wins decisively
// over a look-alike that merely shares the same shape. A lone structural
// twin (count 1) is descended into as the best available target even at
// zero overlap — that covers wrapper groups whose only child is itself a
// deeply value-edited subgroup.
func chooseDescendTarget(arr []any, sk Fingerprint, captured []Fingerprint) (int, string) {
	if sk == "" {
		return -1, "none"
	}
	capCounts := fpCounts(captured)
	bestIdx, bestScore := -1, -1
	tie := false
	for i, e := range arr {
		if Skeleton(e) != sk {
			continue
		}
		m, _ := e.(map[string]any)
		score := overlapScore(capCounts, childFingerprintCounts(m))
		switch {
		case score > bestScore:
			bestIdx, bestScore, tie = i, score, false
		case score == bestScore:
			tie = true
		}
	}
	if bestIdx == -1 {
		return -1, "none"
	}
	if tie {
		return -1, "ambiguous"
	}
	return bestIdx, "unique"
}

// fpCounts turns a fingerprint slice into a multiset (count per fp) for
// overlapScore.
func fpCounts(fps []Fingerprint) map[Fingerprint]int {
	out := map[Fingerprint]int{}
	for _, fp := range fps {
		if fp != "" {
			out[fp]++
		}
	}
	return out
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
