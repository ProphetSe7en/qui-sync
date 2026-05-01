package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNewClient stands in for a real Qui server in tests that don't
// reach runOnce. The scheduler tests below assert registration shape,
// not fire behaviour, so a nil-returning factory is enough.
func fakeNewClient() (*QuiClient, error) { return nil, nil }

// configWithSchedule returns a Config carrying one BackupSchedule.
// Most tests need exactly one — this keeps the per-test scaffolding
// out of every assertion.
func configWithSchedule(s BackupSchedule) *Config {
	return &Config{BackupSchedules: []BackupSchedule{s}}
}

// TestSchedulerStart_NoImmediateFire pins down the bug-fix contract:
// starting the scheduler must NOT trigger a backup, even when the
// configured cron's last slot is in the past — which is exactly the
// post-restart scenario that triggered the v0.2 catch-up bug. The
// fire counter is wired BEFORE Start runs so any spurious dispatch
// (including dispatch that completes before the test could swap
// callbacks) lands in the counter.
func TestSchedulerStart_NoImmediateFire(t *testing.T) {
	cfg := configWithSchedule(BackupSchedule{
		ID: "tv", Name: "TV", Cron: "0 22 * * 1,5", Enabled: true,
	})
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	var fires int32
	b.runOnceFn = func(_ context.Context, _ string) { atomic.AddInt32(&fires, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)

	waitForEntries(t, b, 1)

	// 0 22 * * 1,5 won't naturally fire within the test's lifetime —
	// any non-zero count is a regression.
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt32(&fires); got != 0 {
		t.Fatalf("scheduler fired %d times during Start window — should only fire at the configured cron slot", got)
	}
}

// TestSchedulerReload_Disabled drops the entry when Enabled flips
// off. Without this the cron lib would keep firing the previously-
// registered schedule even after the user disabled it.
func TestSchedulerReload_Disabled(t *testing.T) {
	cfg := configWithSchedule(BackupSchedule{
		ID: "tv", Name: "TV", Cron: "0 22 * * 1,5", Enabled: true,
	})
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)
	waitForEntries(t, b, 1)

	cfg.BackupSchedules[0].Enabled = false
	b.Reload()

	b.mu.Lock()
	defer b.mu.Unlock()
	if got := len(b.entries); got != 0 {
		t.Fatalf("entries map has %d after disable; want 0", got)
	}
	if got := len(b.cron.Entries()); got != 0 {
		t.Fatalf("cron has %d entries after disable; want 0", got)
	}
}

// TestSchedulerReload_CronSwap removes the stale entry before adding
// the new one — no overlap window where two callbacks could race for
// the same slot.
func TestSchedulerReload_CronSwap(t *testing.T) {
	cfg := configWithSchedule(BackupSchedule{
		ID: "tv", Name: "TV", Cron: "0 3 * * *", Enabled: true,
	})
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)
	waitForEntries(t, b, 1)

	b.mu.Lock()
	firstID := b.entries["tv"].id
	b.mu.Unlock()

	cfg.BackupSchedules[0].Cron = "0 22 * * 1,5"
	b.Reload()

	b.mu.Lock()
	defer b.mu.Unlock()
	if got := len(b.entries); got != 1 {
		t.Fatalf("entries map has %d after cron swap; want 1", got)
	}
	if b.entries["tv"].id == firstID {
		t.Fatal("EntryID didn't change on Reload — old entry may still be live")
	}
	if got := len(b.cron.Entries()); got != 1 {
		t.Fatalf("cron has %d entries after swap; want 1", got)
	}
}

// TestSchedulerReload_InvalidCron treats malformed expressions as
// "schedule inactive" rather than crashing the worker.
func TestSchedulerReload_InvalidCron(t *testing.T) {
	cfg := configWithSchedule(BackupSchedule{
		ID: "tv", Name: "TV", Cron: "not a cron", Enabled: true,
	})
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	b.mu.Lock()
	defer b.mu.Unlock()
	if got := len(b.entries); got != 0 {
		t.Fatalf("malformed cron registered %d entries; want 0", got)
	}
}

// TestSchedulerReload_EmptyCron is the "Settings opened but cron
// blank" state — Enabled=true, Cron="". Treat as inactive rather
// than registering a no-op entry.
func TestSchedulerReload_EmptyCron(t *testing.T) {
	cfg := configWithSchedule(BackupSchedule{
		ID: "tv", Name: "TV", Cron: "   ", Enabled: true,
	})
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	b.mu.Lock()
	defer b.mu.Unlock()
	if got := len(b.entries); got != 0 {
		t.Fatalf("empty cron registered %d entries; want 0", got)
	}
}

// TestSchedulerReload_MultipleSchedules verifies independent entries
// per schedule. Adding a second schedule must not disturb the first;
// removing one must not affect the other.
func TestSchedulerReload_MultipleSchedules(t *testing.T) {
	cfg := &Config{BackupSchedules: []BackupSchedule{
		{ID: "tv", Name: "TV", Cron: "0 3 * * *", Enabled: true},
		{ID: "movies", Name: "Movies", Cron: "0 4 * * *", Enabled: true},
	}}
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Start(ctx)
	waitForEntries(t, b, 2)

	// Snapshot the original entry IDs before we mutate the list.
	b.mu.Lock()
	tvID := b.entries["tv"].id
	moviesID := b.entries["movies"].id
	b.mu.Unlock()

	// Drop the second schedule entirely.
	cfg.BackupSchedules = cfg.BackupSchedules[:1]
	b.Reload()

	b.mu.Lock()
	defer b.mu.Unlock()
	if got := len(b.entries); got != 1 {
		t.Fatalf("entries map has %d after dropping movies; want 1", got)
	}
	if _, ok := b.entries["movies"]; ok {
		t.Fatal("movies still in entries map after removal")
	}
	if e := b.entries["tv"]; e.id != tvID {
		t.Fatalf("tv EntryID changed (was %d, now %d) — Reload re-registered an unchanged entry", tvID, e.id)
	}
	_ = moviesID // referenced only to confirm we captured both before mutation
}

// TestRunOnce_DispatchesByID confirms fireCallback passes the correct
// schedule ID through to runOnceFn — important because runOnce uses
// the ID to look up the schedule's instance set.
func TestRunOnce_DispatchesByID(t *testing.T) {
	cfg := &Config{BackupSchedules: []BackupSchedule{
		{ID: "tv", Cron: "* * * * *", Enabled: true},
	}}
	b := NewBackupScheduler(func() *Config { return cfg }, fakeNewClient)

	dispatched := make(chan string, 1)
	b.runOnceFn = func(_ context.Context, id string) { dispatched <- id }

	// Invoke the callback directly rather than waiting for cron to fire.
	b.mu.Lock()
	b.parentCtx = context.Background()
	b.mu.Unlock()
	b.fireCallback("tv")

	select {
	case got := <-dispatched:
		if got != "tv" {
			t.Fatalf("dispatched ID = %q; want tv", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runOnceFn was not invoked within 200ms")
	}
}

func waitForEntries(t *testing.T, b *BackupScheduler, want int) {
	t.Helper()
	deadline := time.After(500 * time.Millisecond)
	for {
		b.mu.Lock()
		got := len(b.entries)
		b.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("scheduler entry count = %d after 500ms; want %d", got, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
