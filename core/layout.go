package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RepoLayout identifies which on-disk format a share-repo uses. qui-sync
// supports its native layout and the TRaSH/qui_workflows layout so the
// same tool can consume or contribute to either.
type RepoLayout string

const (
	// LayoutQuiSync stores rules at rules/<category>/<slug>.json with
	// kebab-case slugs derived from the rule name.
	//
	//	rules/movies/tag-tier1.json
	//	rules/series/resume-stopped-cross-seeds-greater-90pct.json
	LayoutQuiSync RepoLayout = "qui-sync"

	// LayoutTRaSH mirrors TRaSH-/qui_workflows — category directories at
	// the repo root, filenames matching the rule name with filesystem-
	// unsafe characters stripped.
	//
	//	movies/Tag Tier 1.json
	//	series/Resume stopped cross-seeds (greater 90%).json
	LayoutTRaSH RepoLayout = "trash"

	// LayoutUnknown is for empty repos. The writer treats this as
	// "default to qui-sync layout" — first export commits our native
	// layout, and future detections will then return LayoutQuiSync.
	LayoutUnknown RepoLayout = "unknown"
)

// DetectLayout inspects a repo directory and decides which layout its
// contents match. Detection order: qui-sync wins over TRaSH if both
// somehow coexist (that should not happen but guards against accidental
// mixed state), and a repo with neither layout falls through to Unknown.
func DetectLayout(repoDir string) RepoLayout {
	rulesDir := filepath.Join(repoDir, "rules")
	if hasJSONFiles(rulesDir) {
		return LayoutQuiSync
	}
	// TRaSH uses category directories at the repo root. We look for the
	// two canonical ones ("movies" / "series") — that covers every known
	// TRaSH fork and avoids false positives from unrelated top-level
	// dirs like "docs" or "scripts".
	for _, dir := range []string{"movies", "series"} {
		if hasJSONFiles(filepath.Join(repoDir, dir)) {
			return LayoutTRaSH
		}
	}
	return LayoutUnknown
}

// hasJSONFiles returns true if dir contains at least one .json file at
// any depth. Used to distinguish "directory exists but empty" from "this
// layout is active".
func hasJSONFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) == ".json" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// RulePath returns the relative path where a rule with the given
// category + slug + name should live under the configured layout. Slug
// is used for qui-sync layout; name (sanitized) is used for TRaSH layout
// so the files match upstream conventions.
func (l RepoLayout) RulePath(category, slug, name string) string {
	switch l {
	case LayoutTRaSH:
		return category + "/" + TRaSHFilename(name) + ".json"
	default:
		// qui-sync layout (default for Unknown so fresh exports land
		// in our native format).
		return "rules/" + category + "/" + slug + ".json"
	}
}

// IsRulePath returns true when p points at a rule file under the given
// layout. Used by the walker / rule-diff to decide which files in the
// repo participate in a diff.
func (l RepoLayout) IsRulePath(p string) bool {
	if filepath.Ext(p) != ".json" {
		return false
	}
	parts := strings.Split(p, "/")
	switch l {
	case LayoutTRaSH:
		// movies/foo.json or series/foo.json at repo root.
		return len(parts) == 2 && (parts[0] == "movies" || parts[0] == "series")
	case LayoutQuiSync, LayoutUnknown:
		// rules/<cat>/<file>.json with at least one subdir.
		return len(parts) >= 3 && parts[0] == "rules" && parts[len(parts)-1] != ""
	}
	return false
}

// CategoryFromPath extracts the category component from a rule file
// path, respecting the layout's structure.
func (l RepoLayout) CategoryFromPath(relPath string) string {
	parts := strings.Split(relPath, "/")
	switch l {
	case LayoutTRaSH:
		if len(parts) >= 2 {
			return parts[0]
		}
	case LayoutQuiSync, LayoutUnknown:
		if len(parts) >= 3 {
			return strings.Join(parts[1:len(parts)-1], "/")
		}
	}
	return ""
}

// WalkRoots returns the directories (relative to the repo root) this
// layout places rule files under. Used by consumers who want to iterate
// the repo without knowing layout specifics — they walk each returned
// root and filter with IsRulePath.
func (l RepoLayout) WalkRoots() []string {
	switch l {
	case LayoutTRaSH:
		return []string{"movies", "series"}
	default:
		return []string{"rules"}
	}
}

// trashUnsafe matches characters Windows / POSIX won't allow in a
// filename — TRaSH-style filenames are sanitised by deleting these
// outright (which matches the observed upstream convention of removing
// ":" from "Tag: Tier 1").
var trashUnsafe = regexp.MustCompile(`[:/\\?*<>|"]`)

// TRaSHFilename converts a rule name into the on-disk filename used by
// the TRaSH layout. Spaces and case are preserved; filesystem-unsafe
// characters are removed. Multiple spaces collapse to one so round-trip
// consistency survives double-space rule names.
func TRaSHFilename(name string) string {
	s := strings.TrimSpace(name)
	s = trashUnsafe.ReplaceAllString(s, "")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// NameFromTRaSHFilename is the reverse of TRaSHFilename — it strips the
// .json extension from a TRaSH filename to produce a best-effort display
// name. For exact round-trip the rule's "name" field inside the JSON is
// always authoritative; this helper is used when no JSON has been read
// yet (e.g. when listing remote files via git ls-tree).
func NameFromTRaSHFilename(filename string) string {
	return strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
}
