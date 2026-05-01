package core

import (
	"context"
	"log"
	"time"
)

// AutoSyncWorker runs in a background goroutine. On each tick it pulls
// every subscription, plans against the stored target instance, and
// applies only rules marked auto_sync=true. New rules (not in state)
// are always skipped — they need manual review first.
//
// The worker reads the pull interval from cfg on every tick so config
// changes (via UI reload) take effect without restart.
type AutoSyncWorker struct {
	cfg           func() *Config        // live config getter (Server.getConfig)
	consumerState func() *ConsumerState // live state getter
	newClient     func() (*QuiClient, error)
	stopCh        chan struct{}
}

func NewAutoSyncWorker(
	getCfg func() *Config,
	getState func() *ConsumerState,
	newClient func() (*QuiClient, error),
) *AutoSyncWorker {
	return &AutoSyncWorker{
		cfg:           getCfg,
		consumerState: getState,
		newClient:     newClient,
		stopCh:        make(chan struct{}),
	}
}

// Start begins the background loop. Call from main goroutine after
// the HTTP server is up. Blocks until Stop() is called or ctx is
// cancelled.
func (w *AutoSyncWorker) Start(ctx context.Context) {
	log.Println("[auto-sync] worker started")
	// Check every 30 seconds whether it's time to do a sync. This is
	// cheaper than recalculating a dynamic timer when config changes.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var lastRun time.Time

	for {
		select {
		case <-ctx.Done():
			log.Println("[auto-sync] worker stopped (context)")
			return
		case <-w.stopCh:
			log.Println("[auto-sync] worker stopped (explicit)")
			return
		case <-ticker.C:
			interval := ParsePullInterval(w.cfg().AutoPullInterval)
			if interval == 0 {
				continue // auto-sync disabled
			}
			if time.Since(lastRun) < interval {
				continue // not yet time
			}
			lastRun = time.Now()
			w.runOnce(ctx)
		}
	}
}

func (w *AutoSyncWorker) Stop() {
	close(w.stopCh)
}

func (w *AutoSyncWorker) runOnce(ctx context.Context) {
	cfg := w.cfg()
	state := w.consumerState()
	client, err := w.newClient()
	if err != nil {
		log.Printf("[auto-sync] client error: %v", err)
		return
	}

	slugs := state.ListSubscriptionSlugs()
	if len(slugs) == 0 {
		return
	}

	for _, slug := range slugs {
		if ctx.Err() != nil {
			return
		}
		// Skip the synthetic __local__ subscription — it's a symlink to
		// the maintainer's own /data/repo working tree, used only by the
		// "Apply local rules to my own Qui" flow on the Publish tab.
		// Auto-syncing it would call FetchSubscription → git fetch +
		// reset --hard FETCH_HEAD on the maintainer's working tree,
		// wiping any locally-exported but unpushed rule files.
		if slug == "__local__" {
			continue
		}
		sub := state.SubscriptionSnapshot(slug)
		if sub == nil || sub.TargetInstanceID == 0 {
			continue // no target set — skip
		}

		// Pull
		newSHA, err := PullSubscription(ctx, cfg, state, slug)
		if err != nil {
			log.Printf("[auto-sync] pull %s: %v", slug, err)
			continue
		}

		// Plan
		plan, err := PlanSync(ctx, cfg, client, state, slug, sub.TargetInstanceID)
		if err != nil {
			log.Printf("[auto-sync] plan %s: %v", slug, err)
			continue
		}

		// Build decisions: only auto_sync=true rules that have a stored
		// decision (from a previous manual apply). New rules and
		// manual-only rules are skipped entirely.
		var decisions []SyncApplyDecision
		for _, pr := range plan.Rules {
			if !pr.FromState {
				continue // new rule, not seen before — manual only
			}
			entry := sub.Rules[pr.RepoPath]
			if entry == nil || !entry.AutoSync {
				continue
			}
			if entry.LinkedAction == "skip" {
				continue
			}
			// Filter by target category if set.
			if sub.TargetCategory != "" && pr.Category != sub.TargetCategory {
				continue
			}
			decisions = append(decisions, SyncApplyDecision{
				RepoPath:  pr.RepoPath,
				Action:    pr.Action,
				QuiRuleID: pr.QuiRuleID,
				SortOrderOverride: func() *int {
					if pr.SortOrderFrom == "override" {
						v := pr.SortOrder
						return &v
					}
					return nil
				}(),
			})
		}

		if len(decisions) == 0 {
			log.Printf("[auto-sync] %s: pulled %s, no auto-sync rules with changes", slug, newSHA[:7])
			continue
		}

		result, err := ApplySync(ctx, cfg, client, state, slug, SyncApplyRequest{
			QuiInstanceID: sub.TargetInstanceID,
			Decisions:     decisions,
		})
		if err != nil {
			log.Printf("[auto-sync] apply %s: %v", slug, err)
			continue
		}

		nC := len(result.Created)
		nU := len(result.Updated)
		nF := len(result.Failed)
		log.Printf("[auto-sync] %s: %d created, %d updated, %d failed", slug, nC, nU, nF)
	}
}
