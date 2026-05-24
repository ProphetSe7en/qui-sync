package customize

import (
	"sync"
)

// saveLocks serializes Save/Delete operations per (subscription, slug)
// pair so concurrent capture-then-save calls from two HTTP handlers
// don't race on the .tmp file or clobber each other on rename.
// Different (sub, slug) pairs hold independent locks and proceed in
// parallel.
//
// Lock granularity is intentionally narrow: a coarser per-subscription
// or global lock would serialize unrelated writes (e.g. Phase 1.3's
// bulk-capture flow would suffer). The map grows with the working
// set of customized rules but never shrinks — fine in practice since
// rule counts are bounded by what the subscription holds.
//
// Read operations (Load, List, ListForSubscription) intentionally do
// NOT take the lock — they're already safe because each call re-reads
// from disk atomically. A reader can race with a writer and see
// either the pre-write or post-write state, but never a torn read
// (the writer's atomic rename guarantees that).
var (
	saveLocksMu sync.Mutex
	saveLocks   = map[string]*sync.Mutex{}
)

// SaveLockFor returns the per-(sub, slug) mutex, creating it if not
// yet present. Exported so handlers can hold the lock across a
// compute-then-save sequence (e.g. capture: read live + diff + save
// must be atomic per key). For pure Save/Delete sites, prefer
// SaveLocked / DeleteLocked which hold the lock only around the
// storage call itself.
func SaveLockFor(sub, slug string) *sync.Mutex {
	key := sub + "/" + slug
	saveLocksMu.Lock()
	defer saveLocksMu.Unlock()
	m, ok := saveLocks[key]
	if !ok {
		m = &sync.Mutex{}
		saveLocks[key] = m
	}
	return m
}

// SaveLocked is Save wrapped with the per-(sub, slug) mutex. Use this
// from HTTP handlers — Save itself is left lock-free so internal call
// sites that already hold a wider lock (e.g. a future bulk-capture
// path) can call it without nested-lock complications.
func SaveLocked(stateDir, sub, slug string, c *Customization) error {
	mu := SaveLockFor(sub, slug)
	mu.Lock()
	defer mu.Unlock()
	return Save(stateDir, sub, slug, c)
}

// DeleteLocked is Delete wrapped with the per-(sub, slug) mutex.
// Same rationale as SaveLocked — handler convenience.
func DeleteLocked(stateDir, sub, slug string) error {
	mu := SaveLockFor(sub, slug)
	mu.Lock()
	defer mu.Unlock()
	return Delete(stateDir, sub, slug)
}
