package core

import (
	"context"
	"log"
	"strings"
	"time"
)

// BackupScheduler runs a background goroutine that periodically
// snapshots every configured Qui export instance and prunes older
// snapshots according to the retention policy. The cadence is read
// from cfg.Backup.Interval on every tick so config changes take
// effect without restarting the container.
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
	initial := strings.TrimSpace(b.cfg().Backup.Interval)
	if initial == "" || initial == "off" {
		log.Println("[backup] scheduler started (interval=off, waiting for config)")
	} else {
		log.Printf("[backup] scheduler started (interval=%s)", initial)
	}

	// 60s poll is cheaper than recomputing a dynamic timer whenever
	// the interval config changes. The actual backup cost is only
	// paid when interval has elapsed.
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// lastInterval tracks the string value so we can log whenever the
	// user flips the schedule in Settings — useful when diagnosing
	// "I set it to 24h, did anything happen?" reports.
	lastInterval := initial
	var lastRun time.Time

	for {
		select {
		case <-ctx.Done():
			log.Println("[backup] scheduler stopped (context)")
			return
		case <-b.stopCh:
			log.Println("[backup] scheduler stopped (explicit)")
			return
		case <-ticker.C:
			cfg := b.cfg()
			current := strings.TrimSpace(cfg.Backup.Interval)
			if current != lastInterval {
				log.Printf("[backup] interval changed: %q → %q", lastInterval, current)
				lastInterval = current
			}
			interval := ParseBackupInterval(cfg.Backup.Interval)
			if interval == 0 {
				continue // scheduler disabled
			}
			if time.Since(lastRun) < interval {
				continue // not yet time
			}
			lastRun = time.Now()
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
