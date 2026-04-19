package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// backupFixture lays out a set of fake snapshots under
// /data/backups-full/<instanceID>/<ts>/ for retention tests. The
// timestamps are generated backwards from "now" at one-day steps so
// tests can be written in "five days ago" terms.
func backupFixture(t *testing.T, cfg *Config, instanceID int, now time.Time, daysAgo ...int) []string {
	t.Helper()
	var timestamps []string
	for _, d := range daysAgo {
		ts := now.Add(-time.Duration(d) * 24 * time.Hour).Format(BackupTimestampLayout)
		dir := filepath.Join(InstanceBackupDir(cfg, instanceID), ts)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// One placeholder file so the directory is not empty.
		if err := os.WriteFile(filepath.Join(dir, "marker.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		timestamps = append(timestamps, ts)
	}
	return timestamps
}

func newBackupTestCfg(t *testing.T) *Config {
	t.Helper()
	return &Config{RepoDir: filepath.Join(t.TempDir(), "repo")}
}

// TestPruneRetentionDaysOnly deletes everything strictly older than
// N days when no count floor is set.
func TestPruneRetentionDaysOnly(t *testing.T) {
	cfg := newBackupTestCfg(t)
	now := time.Now()
	_ = backupFixture(t, cfg, 1, now, 1, 3, 10, 30, 100)

	deleted, err := PruneInstanceBackups(cfg, 1, 7, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 3 { // 10, 30, 100 days all outside 7-day window
		t.Errorf("expected 3 deletions, got %d: %v", len(deleted), deleted)
	}
	remaining, _ := ListInstanceBackups(cfg, 1)
	if len(remaining) != 2 { // 1 and 3 days ago survive
		t.Errorf("expected 2 remaining, got %d: %v", len(remaining), remaining)
	}
}

// TestPruneKeepLastNOnly keeps the newest N regardless of age.
func TestPruneKeepLastNOnly(t *testing.T) {
	cfg := newBackupTestCfg(t)
	now := time.Now()
	_ = backupFixture(t, cfg, 1, now, 0, 1, 2, 3, 4)

	deleted, err := PruneInstanceBackups(cfg, 1, 0, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 { // 2 oldest evicted (3 and 4 days ago)
		t.Errorf("expected 2 deletions, got %d: %v", len(deleted), deleted)
	}
	remaining, _ := ListInstanceBackups(cfg, 1)
	if len(remaining) != 3 { // newest 3 survive
		t.Errorf("expected 3 remaining, got %d: %v", len(remaining), remaining)
	}
}

// TestPruneBothFloorHonoured — the keep-last-N floor must protect
// snapshots that are older than the retention window.
func TestPruneBothFloorHonoured(t *testing.T) {
	cfg := newBackupTestCfg(t)
	now := time.Now()
	// All five are older than 7-day window but user wants at least 3 kept.
	_ = backupFixture(t, cfg, 1, now, 10, 20, 30, 40, 50)

	deleted, err := PruneInstanceBackups(cfg, 1, 7, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	// Only the two oldest should go — the three newest are floor-protected.
	if len(deleted) != 2 {
		t.Errorf("expected 2 deletions, got %d: %v", len(deleted), deleted)
	}
	remaining, _ := ListInstanceBackups(cfg, 1)
	if len(remaining) != 3 {
		t.Errorf("expected 3 remaining, got %d: %v", len(remaining), remaining)
	}
}

// TestPruneBothFloorAboveRetention — when the user keeps more than age
// alone would leave, the count floor wins without error.
func TestPruneBothFloorAboveRetention(t *testing.T) {
	cfg := newBackupTestCfg(t)
	now := time.Now()
	_ = backupFixture(t, cfg, 1, now, 1, 2, 3, 10, 20)

	deleted, err := PruneInstanceBackups(cfg, 1, 7, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	// Count floor is 10 but only 5 snapshots exist — nothing to delete.
	if len(deleted) != 0 {
		t.Errorf("expected 0 deletions, got %d: %v", len(deleted), deleted)
	}
	remaining, _ := ListInstanceBackups(cfg, 1)
	if len(remaining) != 5 {
		t.Errorf("expected 5 remaining, got %d", len(remaining))
	}
}

// TestPruneNoopWhenBothZero confirms the disabled-retention case.
func TestPruneNoopWhenBothZero(t *testing.T) {
	cfg := newBackupTestCfg(t)
	now := time.Now()
	_ = backupFixture(t, cfg, 1, now, 1, 100, 500)

	deleted, err := PruneInstanceBackups(cfg, 1, 0, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Errorf("both-zero must be a no-op, got deletions: %v", deleted)
	}
	remaining, _ := ListInstanceBackups(cfg, 1)
	if len(remaining) != 3 {
		t.Errorf("expected all 3 remaining, got %d", len(remaining))
	}
}

// TestPruneUnparseableTimestampLeft — garbage directory names (e.g. a
// user-created marker folder) must be left alone; we never risk
// deleting a directory we cannot date.
func TestPruneUnparseableTimestampLeft(t *testing.T) {
	cfg := newBackupTestCfg(t)
	now := time.Now()
	_ = backupFixture(t, cfg, 1, now, 10, 20)
	junkDir := filepath.Join(InstanceBackupDir(cfg, 1), "do-not-touch")
	if err := os.MkdirAll(junkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := PruneInstanceBackups(cfg, 1, 7, 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(junkDir); err != nil {
		t.Errorf("unparseable directory was deleted: %v", err)
	}
}

// TestListInstanceBackupsSkipsTmpDirs ensures in-progress .tmp
// directories created by CreateBackup (and not yet renamed to their
// final timestamp path) never appear as selectable snapshots — they
// are crashed-mid-write backups and a listing that included them
// would mislead retention and restore.
func TestListInstanceBackupsSkipsTmpDirs(t *testing.T) {
	cfg := newBackupTestCfg(t)
	_ = backupFixture(t, cfg, 1, time.Now(), 1, 2)
	// Simulate a crashed backup: a .tmp directory next to the real snapshots.
	tmpDir := filepath.Join(InstanceBackupDir(cfg, 1),
		time.Now().Format(BackupTimestampLayout)+".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	listed, err := ListInstanceBackups(cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range listed {
		if strings.HasSuffix(ts, ".tmp") {
			t.Errorf(".tmp directory must not appear in backup list: %v", listed)
		}
	}
	if len(listed) != 2 {
		t.Errorf("expected 2 real snapshots, got %d: %v", len(listed), listed)
	}
}

// TestParseBackupInterval pins the accepted interval strings.
func TestParseBackupInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"6h":      6 * time.Hour,
		"12h":     12 * time.Hour,
		"24h":     24 * time.Hour,
		"3d":      3 * 24 * time.Hour,
		"7d":      7 * 24 * time.Hour,
		"":        0,
		"off":     0,
		"nonsense": 0,
	}
	for in, want := range cases {
		if got := ParseBackupInterval(in); got != want {
			t.Errorf("ParseBackupInterval(%q) = %v, want %v", in, got, want)
		}
	}
}
