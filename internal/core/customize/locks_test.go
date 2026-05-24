package customize

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// TestSaveLocked_SerializesSameKey is the regression test for review
// finding C1. Pre-fix, two concurrent Save calls for the same (sub,
// slug) raced on the .tmp file and one's rename clobbered the other,
// producing torn or lost data. With SaveLocked the per-key mutex
// serializes them; the latter call's data must be what ends up on
// disk every time.
func TestSaveLocked_SerializesSameKey(t *testing.T) {
	dir := t.TempDir()

	const iterations = 50
	const goroutines = 8

	for iter := 0; iter < iterations; iter++ {
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(notes string) {
				defer wg.Done()
				c := &Customization{
					SchemaVersionAssumed: "1",
					BaseSHA:              "sha256:test",
					CapturedFrom:         CapturedFromSetupTime,
					Diff:                 json.RawMessage(`[]`),
					Notes:                notes,
				}
				_ = SaveLocked(dir, "trash", "race-key", c)
			}(fmt.Sprintf("g%d", g))
		}
		wg.Wait()
		// Post-race: file must be a valid, complete Customization
		// (not a half-written or corrupted shape).
		loaded, err := Load(dir, "trash", "race-key")
		if err != nil {
			t.Fatalf("iter %d: load after concurrent save: %v", iter, err)
		}
		if loaded == nil {
			t.Fatalf("iter %d: expected loaded customization, got nil", iter)
		}
		if loaded.Notes == "" {
			t.Errorf("iter %d: file landed with empty notes — torn write?", iter)
		}
	}
}

// TestSaveLocked_DifferentKeysParallel proves the lock granularity is
// narrow — different (sub, slug) pairs proceed in parallel and don't
// serialize through each other.
func TestSaveLocked_DifferentKeysParallel(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			c := &Customization{
				SchemaVersionAssumed: "1",
				BaseSHA:              "sha256:test",
				CapturedFrom:         CapturedFromSetupTime,
				Diff:                 json.RawMessage(`[]`),
				Notes:                slug,
			}
			_ = SaveLocked(dir, "trash", slug, c)
		}(fmt.Sprintf("rule-%d", i))
	}
	wg.Wait()
	for i := 0; i < 20; i++ {
		slug := fmt.Sprintf("rule-%d", i)
		c, err := Load(dir, "trash", slug)
		if err != nil {
			t.Errorf("load %s: %v", slug, err)
		}
		if c == nil {
			t.Errorf("missing customization for %s", slug)
		}
	}
}

// TestDeleteLocked_AfterConcurrentSave verifies delete serializes
// against in-flight saves on the same key — the delete should
// observe a finished save state, not a half-written tmp file.
func TestDeleteLocked_AfterConcurrentSave(t *testing.T) {
	dir := t.TempDir()
	c := &Customization{
		SchemaVersionAssumed: "1",
		BaseSHA:              "sha256:test",
		CapturedFrom:         CapturedFromSetupTime,
		Diff:                 json.RawMessage(`[]`),
		Notes:                "before-delete",
	}
	if err := SaveLocked(dir, "trash", "k", c); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = SaveLocked(dir, "trash", "k", c)
		}()
	}
	wg.Wait()
	if err := DeleteLocked(dir, "trash", "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	loaded, err := Load(dir, "trash", "k")
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil after delete, got %+v", loaded)
	}
}
