package core

import (
	"testing"

	"github.com/prophetse7en/qui-sync/internal/core/customize"
)

// TestCustomizeAutoPreservedMatchesCore guards the consistency contract
// between core.DefaultPreservedFields() (the canonical Layer-3 list)
// and the customize package's local autoPreservedFields (used by the
// stripAutoPreserved transform). Pre-fix divergence would let a
// customization include diff ops against fields Layer 3 thinks it
// owns — silent data loss on apply.
//
// Lives in the core package (not customize_test) because customize is
// imported by core/sync.go for the apply pipeline. A test in customize
// importing core would create a cycle. core importing customize is
// allowed and is what this file exercises.
func TestCustomizeAutoPreservedMatchesCore(t *testing.T) {
	want := DefaultPreservedFields()
	got := customize.AutoPreservedFields()
	if len(want) != len(got) {
		t.Fatalf("auto-preserved field count diverged: core=%d, customize=%d", len(want), len(got))
	}
	wantSet := make(map[string]bool, len(want))
	for _, f := range want {
		wantSet[f] = true
	}
	for _, f := range got {
		if !wantSet[f] {
			t.Errorf("customize has %q not in core.DefaultPreservedFields()", f)
		}
	}
}
