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
