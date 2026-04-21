package core

import (
	"context"
	"log"
	"strings"
	"time"
)

// BackupScheduler runs a background goroutine that snapshots every
// configured Qui export instance whenever the configured cron
// expression fires, then prunes older snapshots per the retention
// policy. The cron expression and enabled flag are re-read on every
// tick so config changes take effect without restarting the container.
//
// Pattern mirrors AutoSyncWorker — cheap 60-second poll of "is it
// time yet?", config-driven dispatch, graceful stop on ctx cancel.
type BackupScheduler struct {
	cfg       func() *Config
	newClient func() (*QuiClient, error)
	stopCh    chan struct{}
}

func NewBackupScheduler(
	getCfg func() *Config,
	newClient func() (*QuiClient, error),
) *BackupScheduler {
	return &BackupScheduler{
		cfg:       getCfg,
		newClient: newClient,
		stopCh:    make(chan struct{}),
	}
}

// Start begins the background loop. Blocks until Stop() is called or
// ctx is cancelled.
func (b *BackupScheduler) Start(ctx context.Context) {
	cfg := b.cfg()
	initialCron := strings.TrimSpace(cfg.Backup.Cron)
	switch {
	case !cfg.Backup.Enabled:
		log.Println("[backup] scheduler started (disabled — waiting for config)")
	case initialCron == "":
		log.Println("[backup] scheduler started (no cron set — waiting for config)")
	default:
		log.Printf("[backup] scheduler started (cron=%q enabled=true)", initialCron)
	}

	// 60s poll is cheaper than rebuilding a dynamic timer every time the
	// user edits the cron field. The scheduler only dispatches when the
	// cron's next fire time has passed — `time.Ticker` granularity is
	// fine because we're rounding to wall-clock minutes anyway.
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// lastSnapshot is derived from disk so restarts don't burn a fresh
	// backup. Without this, a zero-value time.Time made every restart
	// look infinitely overdue and the next tick fired a backup round
	// immediately — even after an earlier scheduled snapshot had already
	// landed minutes before the restart.
	lastSnapshot := newestBackupTime(cfg)
	if !lastSnapshot.IsZero() {
		log.Printf("[backup] resuming from disk — last snapshot was %s", lastSnapshot.Format(time.RFC3339))
	}

	// lastLoggedState snapshots the cron+enabled combo as a single
	// string so we can log exactly once when the user flips something
	// in Settings, rather than spamming every tick.
	lastLoggedState := schedStateString(cfg)

	for {
		select {
		case <-ctx.Done():
			log.Println("[backup] scheduler stopped (context)")
			return
		case <-b.stopCh:
			log.Println("[backup] scheduler stopped (explicit)")
			return
		case <-ticker.C:
			cfg = b.cfg()
			if s := schedStateString(cfg); s != lastLoggedState {
				log.Printf("[backup] config changed: %s", s)
				lastLoggedState = s
			}
			if !cfg.Backup.Enabled {
				continue
			}
			schedule, err := ParseBackupCron(cfg.Backup.Cron)
			if err != nil {
				continue // invalid / empty — treat as disabled
			}
			// Reference point for the next-fire calculation: start from
			// the last actual snapshot so catching up after downtime
			// still honours the schedule rather than firing every tick.
			// Fall back to "one tick ago" if disk has nothing yet so
			// the very first backup happens at the next cron match.
			ref := lastSnapshot
			if ref.IsZero() {
				ref = time.Now().Add(-60 * time.Second)
			}
			next := schedule.Next(ref)
			if time.Now().Before(next) {
				continue // not yet time
			}
			lastSnapshot = time.Now()
			b.runOnce(ctx)
		}
	}
}

func (b *BackupScheduler) Stop() {
	close(b.stopCh)
}

func (b *BackupScheduler) runOnce(ctx context.Context) {
	cfg := b.cfg()
	if len(cfg.ExportInstances) == 0 {
		return
	}
	client, err := b.newClient()
	if err != nil {
		log.Printf("[backup] client error: %v", err)
		return
	}

	var successCount, failCount int
	for _, inst := range cfg.ExportInstances {
		if ctx.Err() != nil {
			return
		}
		dir, n, err := CreateBackup(ctx, cfg, client, inst.QuiInstanceID)
		if err != nil {
			log.Printf("[backup] instance %d failed: %v", inst.QuiInstanceID, err)
			failCount++
			continue
		}
		log.Printf("[backup] instance %d: %d rules → %s", inst.QuiInstanceID, n, dir)
		successCount++
	}

	// Prune after the backup round so we never delete the safety net
	// of today's snapshot. PruneAllBackups walks all instances, not
	// just those in cfg.ExportInstances, so old instances that were
	// removed from the config still get cleaned up.
	deleted, err := PruneAllBackups(cfg, cfg.Backup.RetentionDays, cfg.Backup.KeepLastN, time.Now())
	if err != nil {
		log.Printf("[backup] prune error (non-fatal): %v", err)
	}
	for id, ts := range deleted {
		log.Printf("[backup] instance %d: pruned %d old snapshots: %v", id, len(ts), ts)
	}

	log.Printf("[backup] scheduler run done — %d ok, %d failed", successCount, failCount)
}

// newestBackupTime scans every export instance's backup directory and
// returns the timestamp of the most recent completed snapshot. Returns
// zero time if no snapshots exist, letting the caller treat a fresh
// install as "run at the next cron match" rather than "backup on boot".
//
// Used by Start() so the scheduler resumes from disk state on restart
// instead of counting from time zero, which caused a backup-storm on
// every container restart when the schedule was enabled.
func newestBackupTime(cfg *Config) time.Time {
	var newest time.Time
	for _, inst := range cfg.ExportInstances {
		timestamps, err := ListInstanceBackups(cfg, inst.QuiInstanceID)
		if err != nil {
			continue
		}
		for _, ts := range timestamps {
			t, err := time.Parse(BackupTimestampLayout, ts)
			if err != nil {
				continue
			}
			if t.After(newest) {
				newest = t
			}
		}
	}
	return newest
}

// schedStateString formats the enabled+cron pair as a single diffable
// string so the scheduler can log exactly once when the user flips
// something in Settings. Kept minimal — the goal is a noise-free
// audit trail in container logs, not a pretty-printer.
func schedStateString(cfg *Config) string {
	if !cfg.Backup.Enabled {
		return "disabled"
	}
	cronExpr := strings.TrimSpace(cfg.Backup.Cron)
	if cronExpr == "" {
		return "enabled (no cron)"
	}
	if _, err := ParseBackupCron(cronExpr); err != nil {
		return "enabled (invalid cron: " + cronExpr + ")"
	}
	return "enabled (cron: " + cronExpr + ")"
}

