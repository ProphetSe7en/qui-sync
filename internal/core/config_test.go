package core

import "testing"

// TestPathsStandardLayout confirms the default repo layout
// (/data/repo) places sibling directories at /data/* — the original
// qui-sync convention.
func TestPathsStandardLayout(t *testing.T) {
	cfg := &Config{RepoDir: "/data/repo"}
	p := cfg.Paths()
	cases := map[string]string{
		"Repo":        "/data/repo",
		"State":       "/data/state",
		"Backups":     "/data/backups",
		"FullBackups": "/data/backups-full",
		"Sources":     "/data/sources",
	}
	got := map[string]string{
		"Repo": p.Repo, "State": p.State, "Backups": p.Backups,
		"FullBackups": p.FullBackups, "Sources": p.Sources,
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// TestPathsCompactLayout covers the case where a user sets RepoDir to
// the volume root (e.g. /data). Without the parent-fallback the
// sibling dirs would compute to /state, /backups, /backups-full,
// /sources — all at filesystem root, which is not writable. The
// fallback pushes them INSIDE RepoDir instead.
func TestPathsCompactLayout(t *testing.T) {
	cfg := &Config{RepoDir: "/data"}
	p := cfg.Paths()
	cases := map[string]string{
		"Repo":        "/data",
		"State":       "/data/state",
		"Backups":     "/data/backups",
		"FullBackups": "/data/backups-full",
		"Sources":     "/data/sources",
	}
	got := map[string]string{
		"Repo": p.Repo, "State": p.State, "Backups": p.Backups,
		"FullBackups": p.FullBackups, "Sources": p.Sources,
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// TestPathsRelativeLayout exercises the other branch of the fallback
// (parent == "."). Rare in practice but the logic lives in the same
// if-branch and we want both cases pinned.
func TestPathsRelativeLayout(t *testing.T) {
	cfg := &Config{RepoDir: "data"}
	p := cfg.Paths()
	if p.FullBackups != "data/backups-full" {
		t.Errorf("FullBackups = %q, want data/backups-full", p.FullBackups)
	}
}
