package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BackupRunStore persists per-schedule "last run" timestamps so the UI
// can show "Last run 2h ago" without scanning every backup folder on
// every list request. The file is intentionally NOT in config.yml
// (which the user edits) — it's runtime state qui-sync owns and
// updates per fire. tmp+rename keeps writes atomic.
type BackupRunStore struct {
	path string

	mu   sync.Mutex
	runs map[string]time.Time
}

type backupRunFile struct {
	Version int                  `json:"version"`
	Runs    map[string]time.Time `json:"runs"`
}

// NewBackupRunStore returns a store that reads/writes <configDir>/
// backup-runs.json. Call Load() before reading; Set/Forget persist
// inline so callers don't need to think about flush semantics.
func NewBackupRunStore(configDir string) *BackupRunStore {
	return &BackupRunStore{
		path: filepath.Join(configDir, "backup-runs.json"),
		runs: make(map[string]time.Time),
	}
}

// Load reads the file into memory. Missing file is not an error —
// returns a fresh empty store, which is the correct first-run state.
func (s *BackupRunStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read backup-runs.json: %w", err)
	}
	var f backupRunFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse backup-runs.json: %w", err)
	}
	if f.Runs != nil {
		s.runs = f.Runs
	}
	return nil
}

// SetLastRun records a fire timestamp for the given schedule and
// persists immediately. Errors propagate so callers can log them —
// failure to record doesn't break the run itself, just the UI hint.
func (s *BackupRunStore) SetLastRun(scheduleID string, when time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[scheduleID] = when
	return s.saveLocked()
}

// LastRun returns the last fire time for a schedule, or zero if the
// schedule has never fired (or has been Forget()ten).
func (s *BackupRunStore) LastRun(scheduleID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[scheduleID]
}

// Forget removes a schedule's run record. Called when a schedule is
// deleted so we don't accumulate dead entries across renames.
func (s *BackupRunStore) Forget(scheduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[scheduleID]; !ok {
		return nil
	}
	delete(s.runs, scheduleID)
	return s.saveLocked()
}

// saveLocked writes the in-memory map to disk atomically. Caller MUST
// hold s.mu — the helper does not lock so SetLastRun + Forget can
// share the critical section with the map mutation.
func (s *BackupRunStore) saveLocked() error {
	f := backupRunFile{Version: 1, Runs: s.runs}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
