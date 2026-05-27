package customize

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageV2_SaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	c := &CustomizationV2{
		RuleSchemaVersion:   "1",
		UpstreamFingerprint: "abc123",
		Notes:               "added upload-exclude",
		Ops: []Op{
			{Kind: OpAdd, Path: []string{"conditions"}, Position: &Position{Kind: "start", FallbackEnd: true}, Value: "x"},
		},
	}
	if err := SaveV2(dir, "movies", "tag-tier1", c); err != nil {
		t.Fatalf("SaveV2: %v", err)
	}
	got, err := LoadV2(dir, "movies", "tag-tier1")
	if err != nil {
		t.Fatalf("LoadV2: %v", err)
	}
	if got == nil {
		t.Fatalf("LoadV2 returned nil")
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion not stamped: got %q", got.SchemaVersion)
	}
	if got.Notes != "added upload-exclude" {
		t.Errorf("Notes lost: %q", got.Notes)
	}
	if len(got.Ops) != 1 || got.Ops[0].Kind != OpAdd {
		t.Errorf("Ops lost: %+v", got.Ops)
	}
	if got.CapturedAt == "" {
		t.Errorf("CapturedAt should be auto-stamped on save")
	}
}

func TestStorageV2_LoadV2_MissingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadV2(dir, "movies", "no-such")
	if err != nil {
		t.Fatalf("LoadV2 on missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file")
	}
}

func TestStorageV2_LoadV2_RejectsV0_5File(t *testing.T) {
	dir := t.TempDir()
	// Drop a v0.5-shaped file directly.
	subDir := filepath.Join(dir, "customizations", "movies")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	v05 := `{"version":1,"schema_version_assumed":"1","diff":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","captured_from":"setup-time"}`
	if err := os.WriteFile(filepath.Join(subDir, "old.json"), []byte(v05), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadV2(dir, "movies", "old")
	if err == nil {
		t.Fatalf("expected ErrUnsupportedV2Version for v0.5 file")
	}
	if !errors.Is(err, ErrUnsupportedV2Version) {
		t.Fatalf("expected ErrUnsupportedV2Version, got %v", err)
	}
}

func TestStorageV2_Delete_Idempotent(t *testing.T) {
	dir := t.TempDir()
	c := &CustomizationV2{Ops: []Op{}}
	if err := SaveV2(dir, "movies", "x", c); err != nil {
		t.Fatal(err)
	}
	if err := DeleteV2(dir, "movies", "x"); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := DeleteV2(dir, "movies", "x"); err != nil {
		t.Fatalf("second Delete (idempotent): %v", err)
	}
	got, _ := LoadV2(dir, "movies", "x")
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestStorageV2_List_SkipsNonV2(t *testing.T) {
	dir := t.TempDir()

	c := &CustomizationV2{Ops: []Op{}}
	if err := SaveV2(dir, "movies", "good", c); err != nil {
		t.Fatal(err)
	}
	// Drop a v0.5 file into the same subscription dir.
	subDir := filepath.Join(dir, "customizations", "movies")
	v05 := `{"version":1,"schema_version_assumed":"1","diff":[],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","captured_from":"setup-time"}`
	if err := os.WriteFile(filepath.Join(subDir, "legacy.json"), []byte(v05), 0o644); err != nil {
		t.Fatal(err)
	}
	// And a malformed JSON file (should be silently skipped, not crash).
	if err := os.WriteFile(filepath.Join(subDir, "garbage.json"), []byte("{this isnt json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListV2ForSubscription(dir, "movies")
	if err != nil {
		t.Fatalf("ListV2: %v", err)
	}
	if len(got) != 1 || got[0] != "good" {
		t.Errorf("expected only 'good' in list, got %v", got)
	}
}

func TestStorageV2_List_EmptyOrMissingSub(t *testing.T) {
	dir := t.TempDir()
	got, err := ListV2ForSubscription(dir, "movies")
	if err != nil {
		t.Fatalf("ListV2 on missing dir: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing subscription, got %v", got)
	}
}

func TestStorageV2_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	c := &CustomizationV2{Ops: []Op{}}
	cases := []struct {
		sub  string
		slug string
	}{
		{"../etc", "x"},
		{"movies", "../etc"},
		{"movies", "foo/bar"},
		{"movies", ".hidden"},
		{"", "x"},
		{"movies", ""},
	}
	for _, tc := range cases {
		err := SaveV2(dir, tc.sub, tc.slug, c)
		if err == nil {
			t.Errorf("expected error for sub=%q slug=%q, got nil", tc.sub, tc.slug)
		}
	}
	// Confirm no files snuck out of the tempdir.
	if strings.Contains(dir, "etc") {
		// Defensive — t.TempDir() never contains "etc" naturally, so
		// the check above is more about the validation not throwing.
		t.Fatalf("unexpected tempdir path: %s", dir)
	}
}

func TestStorageV2_AtomicWrite_NoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	c := &CustomizationV2{Ops: []Op{}}
	if err := SaveV2(dir, "movies", "x", c); err != nil {
		t.Fatal(err)
	}
	// Make sure no .tmp file lingers.
	entries, _ := os.ReadDir(filepath.Join(dir, "customizations", "movies"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("found leftover %s — atomic-write didn't rename cleanly", e.Name())
		}
	}
}
