package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Readable differences inside a nested rule field, such as the
// conditions block. The preview names a field like conditions without a
// value because a condition tree has no honest one-line form; these
// lines are what you get when you open it.
//
// This is deliberately a display-only diff. The customize package has a
// semantic diff too, but it matches array elements by fingerprint so an
// op can say "remove the element with this fingerprint" without saying
// what that element was, which is the one thing a reader needs here.

// maxDiffLines bounds what a single field contributes to a preview.
// Past this the rest is summarised rather than listed.
const maxDiffLines = 12

// diffValueLines describes how old became new, one line per difference.
// Returns nil when the two are equal.
func diffValueLines(old, new any) []string {
	var out []string
	diffValueInto(nil, old, new, &out)
	sort.Strings(out)
	if len(out) > maxDiffLines {
		rest := len(out) - maxDiffLines
		out = append(out[:maxDiffLines:maxDiffLines], fmt.Sprintf("and %d more change%s", rest, plural(rest)))
	}
	return out
}

func diffValueInto(path []string, old, new any, out *[]string) {
	if deepEqualJSON(old, new) {
		return
	}

	oldMap, oldIsMap := old.(map[string]any)
	newMap, newIsMap := new.(map[string]any)
	if oldIsMap && newIsMap {
		keys := map[string]bool{}
		for k := range oldMap {
			keys[k] = true
		}
		for k := range newMap {
			keys[k] = true
		}
		for k := range keys {
			o, hadOld := oldMap[k]
			n, hasNew := newMap[k]
			switch {
			case !hadOld:
				*out = append(*out, fmt.Sprintf("%s added: %s", joinPath(path, k), shortValue(n)))
			case !hasNew:
				*out = append(*out, fmt.Sprintf("%s removed: %s", joinPath(path, k), shortValue(o)))
			default:
				diffValueInto(append(path, k), o, n, out)
			}
		}
		return
	}

	oldArr, oldIsArr := old.([]any)
	newArr, newIsArr := new.([]any)
	if oldIsArr && newIsArr {
		diffArrayInto(path, oldArr, newArr, out)
		return
	}

	// Scalars, or a change of shape. Either way the honest line is what
	// it was and what it is now.
	*out = append(*out, fmt.Sprintf("%s: %s → %s", pathLabel(path), shortValue(old), shortValue(new)))
}

// diffArrayInto compares two lists. Elements are matched by identity
// rather than position, so inserting a condition in the middle reads as
// one addition instead of every later element having moved.
func diffArrayInto(path []string, old, new []any, out *[]string) {
	label := pathLabel(path)
	newByID := map[string][]any{}
	for _, item := range new {
		id := elementIdentity(item)
		newByID[id] = append(newByID[id], item)
	}
	matchedNew := map[string]int{}

	for _, o := range old {
		id := elementIdentity(o)
		candidates := newByID[id]
		idx := matchedNew[id]
		if idx < len(candidates) {
			matchedNew[id]++
			n := candidates[idx]
			if !deepEqualJSON(o, n) {
				// Same identity, different content: say which part moved
				// rather than printing both whole elements.
				sub := diffValueLines(o, n)
				if len(sub) == 0 {
					continue
				}
				for _, line := range sub {
					*out = append(*out, fmt.Sprintf("%s %s: %s", label, shortValue(o), line))
				}
			}
			continue
		}
		*out = append(*out, fmt.Sprintf("%s removed: %s", label, shortValue(o)))
	}

	for id, candidates := range newByID {
		for i := matchedNew[id]; i < len(candidates); i++ {
			*out = append(*out, fmt.Sprintf("%s added: %s", label, shortValue(candidates[i])))
		}
	}
}

// elementIdentity is what makes two list elements "the same thing" for
// diff purposes. Qui's condition leaves are identified by the field and
// operator they test, so a changed threshold reads as a change rather
// than a removal plus an addition. Anything else falls back to its full
// content, which degrades to a plain added/removed pair.
func elementIdentity(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return shortValue(v)
	}
	field, hasField := m["field"]
	op, hasOp := m["operator"]
	if hasField && hasOp {
		return fmt.Sprintf("%v/%v/%v", field, op, m["negate"])
	}
	if name, ok := m["name"]; ok {
		return fmt.Sprintf("name/%v", name)
	}
	return shortValue(v)
}

// pathLabel renders the trail of keys to the value being described.
// An empty path means the field itself changed, which the caller has
// already named, so it reads as "value".
func pathLabel(path []string) string {
	if len(path) == 0 {
		return "value"
	}
	return strings.Join(path, ".")
}

func joinPath(path []string, key string) string {
	return pathLabel(append(append([]string{}, path...), key))
}

// shortValueMaxLen keeps one line readable.
const shortValueMaxLen = 60

// shortValue renders a value for a diff line. A Qui condition leaf gets
// its natural reading, "TAGS EQUAL Tier2"; everything else falls back to
// compact JSON, truncated.
func shortValue(v any) string {
	if m, ok := v.(map[string]any); ok {
		if field, hasField := m["field"]; hasField {
			if op, hasOp := m["operator"]; hasOp {
				s := fmt.Sprintf("%v %v", field, op)
				if val, hasVal := m["value"]; hasVal {
					s += " " + scalarText(val)
				}
				if neg, _ := m["negate"].(bool); neg {
					s = "not " + s
				}
				return s
			}
		}
	}
	if s, ok := v.(string); ok {
		return truncateRunes(s, shortValueMaxLen)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return truncateRunes(string(b), shortValueMaxLen)
}

func scalarText(v any) string {
	if s, ok := v.(string); ok {
		return truncateRunes(s, shortValueMaxLen)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return truncateRunes(string(b), shortValueMaxLen)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
