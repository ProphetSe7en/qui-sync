package core

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// BackupScheduler dispatches a per-schedule snapshot whenever any of
// the configured cron expressions fires. The cron library owns the
// timing — callbacks fire exactly at the next configured slot, never
// at restart or as a "catch-up" for missed slots. Standard cron
// semantics: if the container is down at fire time, that slot is
// skipped, full stop.
//
// Lifecycle:
//   - Start(ctx) wires the cron, registers every active schedule,
//     calls cron.Start, blocks until ctx is cancelled.
//   - Reload() rebuilds the registrations after a Settings save —
//     adds new schedules, drops removed/disabled ones, replaces
//     entries whose cron expression changed.
//   - Stop happens implicitly on ctx cancel — Start drains any
//     in-flight fire via cron.Stop().Done().
//
// Mirrors tagarr/internal/core/scheduler.go's reload-driven multi-
// entry pattern.
type BackupScheduler struct {
	cfg       func() *Config
	newClient func() (*QuiClient, error)
	runs      *BackupRunStore // optional — if nil, last-run isn't persisted
	logger    *log.Logger

	mu         sync.Mutex
	cron       *cron.Cron
	parentCtx  context.Context
	lastLogged string

	// entries maps schedule.ID → cron.EntryID + the cron string used at
	// registration. Reload uses these to detect "no change" (skip
	// re-registration), "expression changed" (drop+add), or "removed
	// from config" (drop). Without this snapshot we'd remove-and-add on
	// every Reload, which is harmless but noisy in the logs.
	entries map[string]scheduledEntry

	// runOnceFn is the function invoked at each scheduled fire. Stored
	// on the struct so tests can swap in a counter without rewiring
	// the cron-callback path.
	runOnceFn func(ctx context.Context, scheduleID string)
}

type scheduledEntry struct {
	id   cron.EntryID
	cron string
}

func NewBackupScheduler(
	getCfg func() *Config,
	newClient func() (*QuiClient, error),
) *BackupScheduler {
	b := &BackupScheduler{
		cfg:       getCfg,
		newClient: newClient,
		logger:    log.New(log.Writer(), "[backup] ", log.LstdFlags),
		entries:   make(map[string]scheduledEntry),
		// Allocate cron up front so Reload() called before Start lands
		// its registrations on a live cron rather than silently no-oping.
		// cron.Start is what kicks the ticker loop — until then,
		// registered entries simply sit idle.
		cron: cron.New(),
	}
	b.runOnceFn = b.runOnce
	return b
}

// Start kicks the cron ticker and blocks until ctx is cancelled. The
// parent context is captured so each fire's runOnce ctx is a child —
// shutdown propagates cleanly even if a backup is mid-flight. If
// Reload was called before Start, its registered entries become live
// here.
func (b *BackupScheduler) Start(ctx context.Context) {
	b.mu.Lock()
	b.parentCtx = ctx
	b.mu.Unlock()

	b.Reload()
	b.cron.Start()
	b.logger.Printf("scheduler started")

	<-ctx.Done()
	stopCtx := b.cron.Stop()
	<-stopCtx.Done()
	b.logger.Printf("scheduler stopped")
}

// Reload syncs cron-entry registrations to BackupSchedules. Safe to
// call from any goroutine and at any time after construction — the
// API config-save handler invokes this so a Settings change takes
// effect immediately.
//
// Disabled, empty, or malformed cron expressions are silently skipped.
// The save-time handler validates Cron-when-Enabled, so a malformed
// expression only reaches here after a hand-edit of config.yml.
func (b *BackupScheduler) Reload() {
	b.mu.Lock()
	defer b.mu.Unlock()

	cfg := b.cfg()

	// Build the wanted set: schedule.ID → desired cron expression. A
	// schedule is "wanted" only if Enabled, has a non-empty Cron, and
	// parses cleanly. Anything else is treated as "remove this entry".
	wanted := make(map[string]string, len(cfg.BackupSchedules))
	for _, s := range cfg.BackupSchedules {
		if s.ID == "" || !s.Enabled {
			continue
		}
		expr := strings.TrimSpace(s.Cron)
		if expr == "" {
			continue
		}
		if _, err := ParseBackupCron(expr); err != nil {
			b.logger.Printf("schedule %q: invalid cron %q: %v — skipped", s.ID, expr, err)
			continue
		}
		wanted[s.ID] = expr
	}

	// Phase 1 — remove stale (deleted, disabled, or cron-changed).
	for id, e := range b.entries {
		w, present := wanted[id]
		stillSame := present && w == e.cron
		if !stillSame {
			b.cron.Remove(e.id)
			delete(b.entries, id)
		}
	}
	// Phase 2 — add new/changed.
	for id, expr := range wanted {
		if _, ok := b.entries[id]; ok {
			continue
		}
		scheduleID := id // capture for closure
		entryID, err := b.cron.AddFunc(expr, func() { b.fireCallback(scheduleID) })
		if err != nil {
			b.logger.Printf("schedule %q: AddFunc failed: %v", id, err)
			continue
		}
		b.entries[id] = scheduledEntry{id: entryID, cron: expr}
	}

	state := schedStateString(cfg, len(b.entries))
	b.logOnChange(state)
}

// fireCallback dispatches one schedule's fire. Reads parentCtx + the
// runOnce hook under lock so a shutdown midway through doesn't race a
// new fire reading a stale ctx.
func (b *BackupScheduler) fireCallback(scheduleID string) {
	b.mu.Lock()
	parent := b.parentCtx
	fn := b.runOnceFn
	b.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	// 30 minutes is generous — a 50-rule instance backup is seconds.
	// Catches a stuck HTTP call without leaving a goroutine forever.
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	fn(ctx, scheduleID)
}

// runOnce is the default backup round invoked at each cron fire. It
// resolves the schedule by ID against the current config, computes
// the instance set (explicit list or "all configured ExportInstances"
// fallback), snapshots each, then prunes per the schedule's retention.
func (b *BackupScheduler) runOnce(ctx context.Context, scheduleID string) {
	cfg := b.cfg()
	sched, ok := findSchedule(cfg, scheduleID)
	if !ok {
		// Schedule was deleted between cron firing and this lookup.
		return
	}
	instances := ResolveScheduleInstances(cfg, sched)
	if len(instances) == 0 {
		b.logger.Printf("%q: no instances to back up", sched.Name)
		return
	}
	client, err := b.newClient()
	if err != nil {
		b.logger.Printf("%q: client error: %v", sched.Name, err)
		return
	}

	var successCount, failCount int
	for _, instID := range instances {
		if ctx.Err() != nil {
			return
		}
		dir, n, err := CreateBackup(ctx, cfg, client, instID)
		if err != nil {
			b.logger.Printf("%q: instance %d failed: %v", sched.Name, instID, err)
			failCount++
			continue
		}
		b.logger.Printf("%q: instance %d: %d rules → %s", sched.Name, instID, n, dir)
		successCount++
	}

	// Prune AFTER the new snapshot lands so today's safety net is
	// always inside the keepLastN floor. Snapshots on disk are not
	// tagged by which schedule made them, so per-schedule retention
	// would silently evict another overlapping schedule's history —
	// e.g. a "TV nightly" with retention=7 firing on instance 1 would
	// delete the "Weekly full" snapshots (retention=365) on the same
	// instance. Compute the union retention (max days, max keep) across
	// every schedule covering this instance and prune once with that.
	// The most-permissive retention wins, which is the only safe
	// interpretation when storage is shared.
	for _, instID := range instances {
		retDays, keepN := EffectiveRetentionForInstance(cfg, instID)
		deleted, err := PruneInstanceBackups(cfg, instID, retDays, keepN, time.Now())
		if err != nil {
			b.logger.Printf("%q: prune instance %d failed (non-fatal): %v", sched.Name, instID, err)
			continue
		}
		if len(deleted) > 0 {
			b.logger.Printf("%q: instance %d: pruned %d old snapshots (effective retention: %dd, keep %d)", sched.Name, instID, len(deleted), retDays, keepN)
		}
	}
	b.logger.Printf("%q: run done — %d ok, %d failed", sched.Name, successCount, failCount)

	// Persist last-run so the UI can show "last run 2h ago" without a
	// folder scan. nil-safe — older constructions skip.
	if b.runs != nil {
		if err := b.runs.SetLastRun(sched.ID, time.Now()); err != nil {
			b.logger.Printf("%q: persist last-run failed (non-fatal): %v", sched.Name, err)
		}
	}
}

// SetRunStore wires the run-store post-construction. main.go calls
// this so the scheduler doesn't need to know about run-store loading
// order. Idempotent.
func (b *BackupScheduler) SetRunStore(rs *BackupRunStore) {
	b.mu.Lock()
	b.runs = rs
	b.mu.Unlock()
}

// findSchedule returns the schedule with the given ID and a bool for
// whether it was found.
func findSchedule(cfg *Config, id string) (BackupSchedule, bool) {
	for _, s := range cfg.BackupSchedules {
		if s.ID == id {
			return s, true
		}
	}
	return BackupSchedule{}, false
}

// EffectiveRetentionForInstance returns the union retention policy
// across every enabled backup schedule that covers the given instance:
// the maximum RetentionDays and the maximum KeepLastN seen across
// all such schedules. Snapshots in <backups-full>/<instance>/<ts>/
// have no per-schedule tag on disk, so prune has to honour the
// most-permissive policy or it would silently delete another
// schedule's history.
//
// Disabled schedules are skipped — the user's intent when toggling
// off is "this rule is paused", which has to extend to its retention
// policy too. Otherwise a paused weekly-retention=365 schedule would
// keep blocking a daily-retention=7 schedule from pruning anything
// past 365 days, defeating the toggle.
//
// Returns (0, 0) when no enabled schedule covers the instance —
// caller passes those to PruneInstanceBackups which is itself a no-op
// for (0, 0).
func EffectiveRetentionForInstance(cfg *Config, instanceID int) (retentionDays, keepLastN int) {
	for _, s := range cfg.BackupSchedules {
		if !s.Enabled {
			continue
		}
		covers := false
		for _, id := range ResolveScheduleInstances(cfg, s) {
			if id == instanceID {
				covers = true
				break
			}
		}
		if !covers {
			continue
		}
		if s.RetentionDays > retentionDays {
			retentionDays = s.RetentionDays
		}
		if s.KeepLastN > keepLastN {
			keepLastN = s.KeepLastN
		}
	}
	return retentionDays, keepLastN
}

// ResolveScheduleInstances returns the instance IDs the schedule
// targets. Explicit Instances list wins; an empty list falls back to
// every configured ExportInstance — preserves the pre-v0.3 single-
// schedule behaviour for migrated configs. Exported so the API
// layer can compute the same set for "Run now" and the per-card
// "n instances" badge without re-deriving the rule.
func ResolveScheduleInstances(cfg *Config, s BackupSchedule) []int {
	if len(s.Instances) > 0 {
		return append([]int(nil), s.Instances...)
	}
	out := make([]int, 0, len(cfg.ExportInstances))
	for _, inst := range cfg.ExportInstances {
		out = append(out, inst.QuiInstanceID)
	}
	return out
}

// logOnChange emits a one-liner when the active schedule set changes
// shape. Without this, every Reload would log even when only an
// unrelated config field changed.
func (b *BackupScheduler) logOnChange(state string) {
	if state == b.lastLogged {
		return
	}
	b.logger.Printf("schedules: %s", state)
	b.lastLogged = state
}

// schedStateString summarises the active schedule set as a one-line
// diffable string. Used only for the once-per-change log line; not a
// pretty-printer.
func schedStateString(cfg *Config, activeEntries int) string {
	if len(cfg.BackupSchedules) == 0 {
		return "none configured"
	}
	enabled := 0
	for _, s := range cfg.BackupSchedules {
		if s.Enabled {
			enabled++
		}
	}
	return strings.TrimSpace(strings.Join([]string{
		"total=" + itoa(len(cfg.BackupSchedules)),
		"enabled=" + itoa(enabled),
		"active=" + itoa(activeEntries),
	}, " "))
}

// itoa is a tiny strconv-free int-to-string for the log helper above.
// Avoids dragging in strconv across files that don't need it.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
