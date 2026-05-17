package core

import (
	"fmt"
	"strings"
)

// BuildExportCommitMessage turns an ExportDiff into a git commit message
// that is useful both in the GitHub commit list (subject line) and when
// reading a PR / commit detail page (body bullets with optional user
// comments). The Publish UI feeds this to GitCommitExport so every
// auto-commit describes what actually changed rather than the generic
// "qui-sync export" placeholder we used to write.
//
// Subject conventions:
//   - 1 change of any kind: "qui-sync: <verb> <category>/<name>"
//     e.g. "qui-sync: rename movies/Foo Tier 1 → Foo Tier 1 (HD)"
//   - 2-N changes of the same kind: "qui-sync export — <verb-ed> N rules"
//     e.g. "qui-sync export — renamed 2 rules"
//   - Mixed kinds: "qui-sync export — N changes"
//
// Body groups changes by section (Added / Updated / Renamed / Archived /
// Moved). When the user attached a per-rule comment it is indented two
// spaces beneath the bullet so GitHub renders it as a continuation line.
func BuildExportCommitMessage(diff *ExportDiff) string {
	if diff == nil || diff.Empty() {
		return "qui-sync export"
	}
	subject := buildCommitSubject(diff)
	body := buildCommitBody(diff)
	if body == "" {
		return subject
	}
	return subject + "\n\n" + body
}

func buildCommitSubject(diff *ExportDiff) string {
	total := len(diff.Added) + len(diff.Updated) + len(diff.Renamed) +
		len(diff.Removed) + len(diff.Moved)

	// Single-change shortcut: surface the actual rule name so the commit
	// list reads like a meaningful history.
	if total == 1 {
		switch {
		case len(diff.Added) == 1:
			e := diff.Added[0]
			return fmt.Sprintf("qui-sync: add %s/%s", e.Category, e.Name)
		case len(diff.Updated) == 1:
			e := diff.Updated[0]
			return fmt.Sprintf("qui-sync: update %s/%s", e.Category, e.Name)
		case len(diff.Renamed) == 1:
			e := diff.Renamed[0]
			return fmt.Sprintf("qui-sync: rename %s/%s → %s", e.Category, e.OldName, e.Name)
		case len(diff.Removed) == 1:
			e := diff.Removed[0]
			return fmt.Sprintf("qui-sync: archive %s/%s", e.Category, e.Name)
		case len(diff.Moved) == 1:
			e := diff.Moved[0]
			return fmt.Sprintf("qui-sync: move %s → %s (%s)", e.OldCategory, e.Category, e.Name)
		}
	}

	// Single-kind shortcut: every change is the same kind, so name the
	// kind and the count.
	switch {
	case allInOne(diff, "added"):
		return fmt.Sprintf("qui-sync export — added %d rule%s", len(diff.Added), plural(len(diff.Added)))
	case allInOne(diff, "updated"):
		return fmt.Sprintf("qui-sync export — updated %d rule%s", len(diff.Updated), plural(len(diff.Updated)))
	case allInOne(diff, "renamed"):
		return fmt.Sprintf("qui-sync export — renamed %d rule%s", len(diff.Renamed), plural(len(diff.Renamed)))
	case allInOne(diff, "removed"):
		return fmt.Sprintf("qui-sync export — archived %d rule%s", len(diff.Removed), plural(len(diff.Removed)))
	case allInOne(diff, "moved"):
		return fmt.Sprintf("qui-sync export — moved %d rule%s", len(diff.Moved), plural(len(diff.Moved)))
	}

	return fmt.Sprintf("qui-sync export — %d changes", total)
}

// allInOne reports whether every diff entry sits in a single named bucket.
// Used by buildCommitSubject to pick the single-kind subject form.
func allInOne(diff *ExportDiff, bucket string) bool {
	total := len(diff.Added) + len(diff.Updated) + len(diff.Renamed) +
		len(diff.Removed) + len(diff.Moved)
	switch bucket {
	case "added":
		return total == len(diff.Added) && total > 0
	case "updated":
		return total == len(diff.Updated) && total > 0
	case "renamed":
		return total == len(diff.Renamed) && total > 0
	case "removed":
		return total == len(diff.Removed) && total > 0
	case "moved":
		return total == len(diff.Moved) && total > 0
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func buildCommitBody(diff *ExportDiff) string {
	var sb strings.Builder

	writeSection(&sb, "Added", diff.Added, func(e DiffEntry) string {
		return fmt.Sprintf("%s/%s", e.Category, e.Name)
	})
	writeSection(&sb, "Updated", diff.Updated, func(e DiffEntry) string {
		return fmt.Sprintf("%s/%s", e.Category, e.Name)
	})
	writeSection(&sb, "Renamed", diff.Renamed, func(e DiffEntry) string {
		return fmt.Sprintf("%s/%s → %s", e.Category, e.OldName, e.Name)
	})
	writeSection(&sb, "Moved", diff.Moved, func(e DiffEntry) string {
		return fmt.Sprintf("%s/%s → %s/%s", e.OldCategory, e.Name, e.Category, e.Name)
	})
	writeSection(&sb, "Archived", diff.Removed, func(e DiffEntry) string {
		return fmt.Sprintf("%s/%s", e.Category, e.Name)
	})

	return strings.TrimRight(sb.String(), "\n")
}

// writeSection emits "<Heading>:" + per-entry bullets. If the user
// attached a comment it appears on the line beneath the bullet, indented
// two spaces so GitHub renders it as part of the same list item.
func writeSection(sb *strings.Builder, heading string, entries []DiffEntry, line func(DiffEntry) string) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(sb, "%s:\n", heading)
	for _, e := range entries {
		fmt.Fprintf(sb, "- %s\n", line(e))
		c := strings.TrimSpace(e.Comment)
		if c == "" {
			continue
		}
		// Multi-line comments: keep paragraphs by indenting every line.
		for _, ln := range strings.Split(strings.ReplaceAll(c, "\r\n", "\n"), "\n") {
			fmt.Fprintf(sb, "  %s\n", ln)
		}
	}
	sb.WriteString("\n")
}
