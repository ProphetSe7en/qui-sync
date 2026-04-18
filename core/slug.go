package core

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
	dashes   = regexp.MustCompile(`-+`)
	// Ordered list (not a map) so replacement is deterministic across Go versions.
	commonSyns = [][2]string{
		{"&", "and"},
		{"+", "plus"},
		{"%", "pct"},
		{"/", "-"},
	}
)

// Slugify converts a rule name into a kebab-case slug.
// It is deterministic: same input → same output.
//
//	"Delete: noHL Tier 1 (21 days)"  -> "delete-nohl-tier1-21-days"
//	"Tag ~Tier1-noHL-21"             -> "tag-tier1-nohl-21"
func Slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	for _, pair := range commonSyns {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	// Collapse any "tier N" / "tier N " into "tierN" for shorter slugs.
	s = regexp.MustCompile(`tier\s*(\d+)`).ReplaceAllString(s, "tier$1")
	s = nonAlnum.ReplaceAllString(s, "-")
	s = dashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// UniqueSlug returns a slug that doesn't collide with existing slugs.
// If the base slug is taken it appends -2, -3, etc.
func UniqueSlug(name string, taken map[string]bool) string {
	base := Slugify(name)
	if base == "" {
		base = "rule"
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if !taken[candidate] {
			return candidate
		}
	}
}
