package api

import (
	"testing"

	"github.com/prophetse7en/qui-sync/internal/core"
)

// A rule whose order you set by hand stops following Qui, which is why a
// repository's order can drift from the one on screen in Qui. Clearing
// the overrides for an instance is the way back, and it must leave other
// instances alone.
func TestClearSortOrderOverridesForInstance(t *testing.T) {
	dir := t.TempDir()
	state, err := core.LoadMaintainerState(dir)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	state.SetSortOrderOverride(1, 10, 27)
	state.SetSortOrderOverride(1, 11, 28)
	state.SetSortOrderOverride(2, 20, 5)

	if n := state.ClearSortOrderOverridesForInstance(1); n != 2 {
		t.Errorf("cleared = %d, want 2", n)
	}
	if _, ok := state.SortOrderOverride(1, 10); ok {
		t.Errorf("rule 10 should follow Qui again")
	}
	if _, ok := state.SortOrderOverride(1, 11); ok {
		t.Errorf("rule 11 should follow Qui again")
	}
	if v, ok := state.SortOrderOverride(2, 20); !ok || v != 5 {
		t.Errorf("the other instance must keep its overrides, got %v ok=%v", v, ok)
	}
	if n := state.ClearSortOrderOverridesForInstance(1); n != 0 {
		t.Errorf("clearing again reported %d, want 0", n)
	}

	// The cleared state has to survive a save and reload, otherwise the
	// old order comes back on the next export.
	if err := state.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := core.LoadMaintainerState(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.SortOrderOverride(1, 10); ok {
		t.Errorf("override came back after reload")
	}
	if v, ok := reloaded.SortOrderOverride(2, 20); !ok || v != 5 {
		t.Errorf("other instance lost its override across reload, got %v ok=%v", v, ok)
	}
}
