package core

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// backupGitignoreComment + perRuleBackupGitignoreComment are the
// marker lines we write to .gitignore when the "Hide backups from
// GitHub" toggle is on. Two paths get their own marker so toggle-off
// can recognise + remove both halves cleanly, and so a future read can
// tell them apart from hand-edited entries.
//
// Both paths are repo-relative. A user with the default layout
// (RepoDir=/data/repo, backups outside the repo as siblings) sees
// EnsureBackupGitignore no-op for that path (gitignore can't reach
// out-of-repo), while a compact layout (RepoDir=/data, backups as
// children) gets the right ignore rule.
const backupGitignoreComment = "# qui-sync: hide full-instance backups from GitHub"

// perRuleBackupGitignoreComment pairs with the "backups/" entry that
// hides per-rule export backups (the files backupFile() drops into
// paths.Backups when a rule is modified/replaced during Export).
// These accumulate on every Export and were not covered by the
// original toggle — TRaSH-/qui_workflows on GitHub had 5+ such files
// at the .gitignore-extended bug-fix date despite the toggle being on.
const perRuleBackupGitignoreComment = "# qui-sync: hide per-rule export backups from GitHub"

// archiveGitignoreComment is the marker comment for the "Hide archived
// rules from GitHub" toggle. Paired with the "archive/" entry so a
// later read can recognise the qui-sync block and remove both halves
// cleanly on toggle-off.
const archiveGitignoreComment = "# qui-sync: hide archived rules from GitHub"

// archiveGitignoreEntry is the constant ignore rule for the archive
// folder. archive/ always lives at the share-repo root by design, so
// the rule is constant — no per-config path computation needed.
const archiveGitignoreEntry = "archive/"

// EnsureBackupGitignore reconciles the share-repo's .gitignore with
// cfg.BackupGitignored. Covers BOTH backup paths in one pass:
//
//   - paths.FullBackups (Backup-tab snapshots, /data/backups-full/)
//   - paths.Backups     (per-rule export backups, /data/backups/)
//
// Per-path behaviour:
//
//   1. Path lives OUTSIDE the repo (typical: RepoDir=/data/repo, both
//      backup dirs are siblings) — gitignore can't reach the folder,
//      so the per-path step is a no-op. No surprise to users who
//      already have the toggle on by default.
//   2. Path lives INSIDE the repo (compact: RepoDir=/data, backups
//      as children) — and the toggle is ON: ensure a "<rel>/" entry
//      exists in .gitignore.
//   3. Same as (2) but the toggle is OFF: ensure the entry is
//      removed so an explicit "I want these published" choice takes
//      effect.
//
// Both paths are reconciled independently in the same .gitignore so
// one toggle controls both — matching what users expect from a label
// that just says "Hide backups from GitHub".
//
// Idempotent. Tolerates a missing repo (creates .gitignore alongside
// nothing) and a missing .gitignore (creates it).
func EnsureBackupGitignore(cfg *Config) error {
	paths := cfg.Paths()
	repoDir := paths.Repo

	giPath := filepath.Join(repoDir, ".gitignore")
	existing, err := os.ReadFile(giPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	updated := existing
	anyChanged := false

	// Apply both backup-path reconciliations to the in-memory buffer
	// before any disk write so a single rename lands the combined
	// result atomically.
	for _, p := range []struct {
		dir     string
		comment string
	}{
		{paths.FullBackups, backupGitignoreComment},
		{paths.Backups, perRuleBackupGitignoreComment},
	} {
		entry, ok := repoRelativeGitignoreEntry(repoDir, p.dir)
		if !ok {
			continue
		}
		next, changed := updateGitignoreEntry(updated, entry, p.comment, cfg.BackupGitignored)
		if changed {
			updated = next
			anyChanged = true
		}
	}

	if !anyChanged {
		return nil
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return err
	}
	tmp := giPath + ".tmp"
	if err := os.WriteFile(tmp, updated, 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return os.Rename(tmp, giPath)
}

// repoRelativeGitignoreEntry converts an absolute backup path into the
// "<rel>/" form .gitignore needs, or returns ok=false when the path
// lives outside the repo (gitignore can't reach it from there).
// Centralises the filepath.Rel + "../"-prefix + trailing-slash logic so
// the same rule applies uniformly to every backup-dir reconciled in
// one EnsureBackupGitignore pass.
func repoRelativeGitignoreEntry(repoDir, backupDir string) (string, bool) {
	rel, err := filepath.Rel(repoDir, backupDir)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(rel, "..") || rel == "." {
		return "", false
	}
	entry := strings.ReplaceAll(rel, string(filepath.Separator), "/")
	if !strings.HasSuffix(entry, "/") {
		entry += "/"
	}
	return entry, true
}

// EnsureArchiveGitignore reconciles the share-repo's .gitignore with
// cfg.ArchiveGitignored. Simpler than EnsureBackupGitignore because the
// archive/ folder always lives at the share-repo root by design —
// there's no out-of-repo edge case to handle.
//
// On=true: ensure "archive/" exists in .gitignore (paired with a
// recognisable qui-sync comment so toggle-off can clean both halves).
// On=false: ensure the entry and its comment are removed.
//
// Idempotent. The write itself is immediate but the user only sees the
// effect on the next git commit (Commit export or any push that stages
// .gitignore). That matches the BackupGitignored UX so the two toggles
// behave consistently.
func EnsureArchiveGitignore(cfg *Config) error {
	paths := cfg.Paths()
	repoDir := paths.Repo
	giPath := filepath.Join(repoDir, ".gitignore")

	existing, err := os.ReadFile(giPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .gitignore: %w", err)
	}

	updated, changed := updateGitignoreEntry(existing, archiveGitignoreEntry, archiveGitignoreComment, cfg.ArchiveGitignored)
	if !changed {
		return nil
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return err
	}
	tmp := giPath + ".tmp"
	if err := os.WriteFile(tmp, updated, 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return os.Rename(tmp, giPath)
}

// updateGitignoreEntry walks the existing .gitignore content line by
// line, ensures the marker comment + entry pair exists when wanted=true,
// and removes both when wanted=false. Returns the new content and a
// bool indicating whether anything actually changed (so callers can
// skip the write+rename when there's nothing to do).
//
// comment is the qui-sync marker that pairs with this entry — the two
// callers (backup, archive) use different markers so a toggle-off for
// one leaves the other intact even though both pass through here.
func updateGitignoreEntry(existing []byte, entry, comment string, wanted bool) ([]byte, bool) {
	lines := splitLines(existing)
	hasEntry := false
	hasComment := false
	out := make([]string, 0, len(lines)+2)
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == entry {
			hasEntry = true
			if !wanted {
				continue // drop on the wanted=false path
			}
		}
		if trimmed == comment {
			hasComment = true
			if !wanted {
				continue
			}
		}
		out = append(out, l)
	}

	if wanted && !hasEntry {
		// Append the comment + entry as a pair so a later read can
		// recognise the qui-sync block. Trim trailing blank lines so
		// the appended block doesn't push existing content around.
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		if !hasComment {
			out = append(out, comment)
		}
		out = append(out, entry)
	}

	newContent := strings.Join(out, "\n")
	if newContent != "" && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	if string(existing) == newContent {
		return existing, false
	}
	return []byte(newContent), true
}

// splitLines is a strings.Split-on-newline that preserves intra-line
// trailing whitespace. bufio.Scanner would strip it; bytes.Split keeps
// it but loses the empty trailing line tracking we need to write
// canonical \n-terminated content back.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	if bytes.HasSuffix(b, []byte("\n")) {
		b = b[:len(b)-1]
	}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	var out []string
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	return out
}
