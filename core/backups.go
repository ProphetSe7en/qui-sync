package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// BackupTimestampLayout is the directory name format for a single
// backup snapshot. Sortable, filesystem-safe, readable at a glance.
const BackupTimestampLayout = "2006-01-02_15-04-05"

// InstanceBackupDir returns the <data-volume>/backups-full/<instance-id>/
// root where that instance's snapshots are stored.
func InstanceBackupDir(cfg *Config, instanceID int) string {
	return filepath.Join(cfg.Paths().FullBackups, strconv.Itoa(instanceID))
}

// CreateBackup snapshots every automation in a Qui instance into a
// timestamped directory under /data/backups-full/<instance-id>/. Full
// 1:1 copy — no stripping, no placeholders. Returns the absolute path
// of the created directory and the number of rules written.
//
// Used by both the manual "Backup now" button and the background
// scheduler so the on-disk shape is identical regardless of trigger.
func CreateBackup(ctx context.Context, cfg *Config, client *QuiClient, instanceID int) (dir string, ruleCount int, err error) {
	if instanceID <= 0 {
		return "", 0, fmt.Errorf("invalid instance id %d", instanceID)
	}
	rules, err := client.ListAutomations(ctx, instanceID)
	if err != nil {
		return "", 0, fmt.Errorf("list automations (instance %d): %w", instanceID, err)
	}
	ts := time.Now().Format(BackupTimestampLayout)
	dir = filepath.Join(InstanceBackupDir(cfg, instanceID), ts)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir backup: %w", err)
	}
	for _, rule := range rules {
		data, err := json.MarshalIndent(json.RawMessage(rule.Raw), "", "  ")
		if err != nil {
			data = rule.Raw
		}
		slug := Slugify(rule.Name)
		fname := fmt.Sprintf("%s_%d.json", slug, rule.ID)
		if err := os.WriteFile(filepath.Join(dir, fname), data, 0o644); err != nil {
			return dir, 0, fmt.Errorf("write %s: %w", fname, err)
		}
	}
	return dir, len(rules), nil
}

// ListInstanceBackups returns the timestamp strings of every backup
// under instance <id>, sorted newest-first. Missing directory returns
// an empty slice without error — nothing to list is not a failure.
func ListInstanceBackups(cfg *Config, instanceID int) ([]string, error) {
	dir := InstanceBackupDir(cfg, instanceID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// PruneInstanceBackups applies the retention policy to a single
// instance's snapshots and returns the list of timestamps deleted.
//
// Retention semantics:
//   - If keepLastN > 0, the newest keepLastN snapshots are always kept
//     regardless of age.
//   - If retentionDays > 0, snapshots older than that age are candidates
//     for removal — but only beyond the keepLastN floor.
//   - Both rules combine: "delete snapshots older than retentionDays AND
//     outside the keepLastN most recent".
//
// When both settings are zero, the function is a no-op — nothing gets
// pruned, matching prior manual-backup-only behaviour.
func PruneInstanceBackups(cfg *Config, instanceID int, retentionDays, keepLastN int, now time.Time) ([]string, error) {
	if retentionDays <= 0 && keepLastN <= 0 {
		return nil, nil
	}
	timestamps, err := ListInstanceBackups(cfg, instanceID)
	if err != nil {
		return nil, err
	}

	// timestamps is sorted newest-first. Keep the first keepLastN as
	// the "protected" floor; consider the rest for age-based eviction.
	var deleted []string
	for i, ts := range timestamps {
		if keepLastN > 0 && i < keepLastN {
			continue
		}
		if retentionDays <= 0 {
			// keepLastN alone: evict everything past the floor.
		} else {
			t, err := time.Parse(BackupTimestampLayout, ts)
			if err != nil {
				// Unparseable timestamp — leave it alone, don't risk
				// deleting a snapshot we can't date.
				continue
			}
			if now.Sub(t) <= time.Duration(retentionDays)*24*time.Hour {
				continue // within retention window
			}
		}
		if err := os.RemoveAll(filepath.Join(InstanceBackupDir(cfg, instanceID), ts)); err != nil {
			return deleted, fmt.Errorf("remove %s: %w", ts, err)
		}
		deleted = append(deleted, ts)
	}
	return deleted, nil
}

// PruneAllBackups enumerates every instance that has a backup directory
// and prunes each according to the shared retention config. Used by the
// scheduler after a successful round of backups.
func PruneAllBackups(cfg *Config, retentionDays, keepLastN int, now time.Time) (map[int][]string, error) {
	base := cfg.Paths().FullBackups
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := map[int][]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		deleted, err := PruneInstanceBackups(cfg, id, retentionDays, keepLastN, now)
		if err != nil {
			return out, err
		}
		if len(deleted) > 0 {
			out[id] = deleted
		}
	}
	return out, nil
}
