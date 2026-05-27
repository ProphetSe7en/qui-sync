package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compactLayoutCfg returns a Config whose Paths() resolves to the
// compact-mode layout (backups INSIDE the repo) by chdir'ing into a
// temp directory and using a single-component relative RepoDir. That
// triggers the `filepath.Dir(c.RepoDir) == "."` fallback in Paths()
// without requiring root or a single-component absolute path like
// /data, which a unit test can't produce.
func compactLayoutCfg(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("repo", 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	return &Config{
		RepoDir:          "repo",
		BackupGitignored: true,
	}
}

// TestEnsureBackupGitignore_CompactCoversBothPaths is the regression
// test for the TRaSH-/qui_workflows soak finding (2026-05-23): with
// the toggle on, per-rule export backups at `backups/` were still
// being pushed to GitHub because EnsureBackupGitignore only ignored
// `backups-full/`. The fix extends the same toggle to cover both
// backup paths so a single "Hide backups from GitHub" check actually
// stops both kinds.
func TestEnsureBackupGitignore_CompactCoversBothPaths(t *testing.T) {
	cfg := compactLayoutCfg(t)
	if err := EnsureBackupGitignore(cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(cfg.RepoDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	s := string(got)
	for _, want := range []string{"backups-full/", "backups/"} {
		if !strings.Contains(s, want) {
			t.Errorf("expected .gitignore to contain %q, got:\n%s", want, s)
		}
	}
	for _, wantComment := range []string{backupGitignoreComment, perRuleBackupGitignoreComment} {
		if !strings.Contains(s, wantComment) {
			t.Errorf("expected .gitignore to contain marker %q, got:\n%s", wantComment, s)
		}
	}
}

// TestEnsureBackupGitignore_ToggleOffRemovesBothEntries verifies the
// reverse: when the toggle flips from on to off, both entries (and
// their marker comments) are cleaned out cleanly. Defends against the
// "I changed my mind, please publish" flow leaving stale entries.
func TestEnsureBackupGitignore_ToggleOffRemovesBothEntries(t *testing.T) {
	cfg := compactLayoutCfg(t)
	if err := EnsureBackupGitignore(cfg); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	cfg.BackupGitignored = false
	if err := EnsureBackupGitignore(cfg); err != nil {
		t.Fatalf("toggle off: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(cfg.RepoDir, ".gitignore"))
	s := string(got)
	for _, mustNotContain := range []string{
		"backups-full/",
		"backups/",
		backupGitignoreComment,
		perRuleBackupGitignoreComment,
	} {
		if strings.Contains(s, mustNotContain) {
			t.Errorf("expected toggle-off to remove %q, still present in:\n%s", mustNotContain, s)
		}
	}
}

// TestEnsureBackupGitignore_DefaultLayoutIsNoOp covers the
// most-common deployment (RepoDir=/data/repo, backups as siblings):
// gitignore can't reach out-of-repo paths so the function should
// write nothing rather than emit a confusing "../backups/" entry.
func TestEnsureBackupGitignore_DefaultLayoutIsNoOp(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cfg := &Config{
		RepoDir:          repoDir,
		BackupGitignored: true,
	}
	if err := EnsureBackupGitignore(cfg); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("expected no .gitignore in default layout (siblings), got err=%v", err)
	}
}

// TestEnsureBackupGitignore_PreservesHandEdits verifies the per-pass
// merge doesn't clobber unrelated lines. Users sometimes add their
// own entries (`.env`, `node_modules/`) — they must survive a toggle
// flip.
func TestEnsureBackupGitignore_PreservesHandEdits(t *testing.T) {
	cfg := compactLayoutCfg(t)
	giPath := filepath.Join(cfg.RepoDir, ".gitignore")
	handWritten := ".env\nnode_modules/\n"
	if err := os.WriteFile(giPath, []byte(handWritten), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	if err := EnsureBackupGitignore(cfg); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, _ := os.ReadFile(giPath)
	s := string(got)
	for _, must := range []string{".env", "node_modules/", "backups/", "backups-full/"} {
		if !strings.Contains(s, must) {
			t.Errorf("expected %q to survive, got:\n%s", must, s)
		}
	}
}
