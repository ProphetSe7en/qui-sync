package core

import "sync"

// repoLocks serializes RunExport calls per repo directory so two concurrent
// server requests on the same repo don't race on state.json, backup/, or
// rules/ writes. Different repos can export in parallel.
//
// The CLI is single-threaded so this is a no-op in practice, but the HTTP
// server needs this guarantee before it can safely expose /api/export/run.
var (
	repoLocksMu sync.Mutex
	repoLocks   = map[string]*sync.Mutex{}
)

func lockForRepo(repoDir string) *sync.Mutex {
	repoLocksMu.Lock()
	defer repoLocksMu.Unlock()
	m, ok := repoLocks[repoDir]
	if !ok {
		m = &sync.Mutex{}
		repoLocks[repoDir] = m
	}
	return m
}

// instanceBackupLocks serializes CreateBackup calls per Qui instance ID.
// CreateBackup names its destination by `time.Now().Format("...")` at
// second resolution, so two firings inside the same second on the same
// instance both pick the same `<ts>` and the same `<ts>.tmp` working
// directory — they'd race on file writes and on the final
// os.Rename(<ts>.tmp, <ts>) finalisation.
//
// In practice this used to be impossible (single scheduler ticking once
// per minute) but with the v0.3 multi-schedule model two schedules
// covering the same instance can fire on the same boundary, and the
// "Run now" button can be clicked while a scheduled run is in flight.
// Different instances still back up in parallel.
var (
	instanceBackupLocksMu sync.Mutex
	instanceBackupLocks   = map[int]*sync.Mutex{}
)

func lockForInstanceBackup(instanceID int) *sync.Mutex {
	instanceBackupLocksMu.Lock()
	defer instanceBackupLocksMu.Unlock()
	m, ok := instanceBackupLocks[instanceID]
	if !ok {
		m = &sync.Mutex{}
		instanceBackupLocks[instanceID] = m
	}
	return m
}
