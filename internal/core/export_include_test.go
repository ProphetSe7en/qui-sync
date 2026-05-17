package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeQuiServer stands in for an upstream Qui so RunExport can exercise
// its include / skip semantics without touching the network. Returns a
// httptest.Server you should Close() in t.Cleanup.
func fakeQuiServer(t *testing.T, automationsByInstance map[int][]Automation) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/instances/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")
		if len(parts) < 2 || parts[1] != "automations" {
			http.NotFound(w, r)
			return
		}
		var instID int
		if _, err := fmt.Sscanf(parts[0], "%d", &instID); err != nil {
			http.NotFound(w, r)
			return
		}
		out, _ := json.Marshal(automationsByInstance[instID])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	return httptest.NewServer(mux)
}

// automation builds an Automation with the minimum Raw payload qui-sync
// needs to roundtrip through buildRuleFile.
func newAutomation(id, instanceID int, name string) Automation {
	raw := json.RawMessage(fmt.Sprintf(`{
		"id": %d,
		"instanceId": %d,
		"name": %q,
		"trackerPattern": "tracker.example.com",
		"trackerDomains": ["tracker.example.com"],
		"conditions": {"schemaVersion": "1"},
		"intervalSeconds": 300
	}`, id, instanceID, name))
	a := Automation{ID: id, InstanceID: instanceID, Name: name}
	a.Raw = raw
	return a
}

// setupExportFixture creates a Config + writes the original rule file +
// seeds maintainer state with one entry. Returns the Config so callers
// can drive RunExport directly.
func setupExportFixture(t *testing.T, srvURL, oldName string) (*Config, string) {
	t.Helper()
	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	for _, d := range []string{repoDir, filepath.Join(repoDir, "movies")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &Config{
		RepoDir:         repoDir,
		Qui:             QuiConfig{URL: srvURL, APIKey: "test"},
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "movies"}},
	}
	paths := cfg.Paths()
	if err := os.MkdirAll(paths.State, 0o755); err != nil {
		t.Fatal(err)
	}

	state := &MaintainerState{Version: 1, Instances: map[string]*InstanceStateData{}}
	state.Assign(1, 42, "old", oldName, "movies")
	if err := state.Save(paths.State); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return cfg, repoDir
}

// TestRunExportIncludeFilterSkipsRulesEndToEnd locks down the headline
// promise of the per-row checkbox: when the user deselects a row, the
// export must not touch state, must not write the file to disk, must not
// add the entry to the returned diff, and must surface the same change
// on the next Preview so the user has a chance to revisit it.
func TestRunExportIncludeFilterSkipsRulesEndToEnd(t *testing.T) {
	srv := fakeQuiServer(t, map[int][]Automation{
		1: {newAutomation(42, 1, "Old (HD)")},
	})
	t.Cleanup(srv.Close)

	cfg, repoDir := setupExportFixture(t, srv.URL, "Old")

	// Drop a file at the path qui-sync expects for the pre-rename rule.
	// Test passes only if file content is preserved across the skipped export.
	originalContent := []byte(`{"_slug":"old","name":"Old","sortOrder":0}`)
	if err := os.WriteFile(filepath.Join(repoDir, "movies", "Old.json"), originalContent, 0o644); err != nil {
		t.Fatal(err)
	}

	client := NewQuiClient(srv.URL, "test")

	// Include map empty → exclude everything.
	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{
		Include: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if !diff.Empty() {
		t.Fatalf("expected empty diff after full skip, got renamed=%d updated=%d added=%d removed=%d moved=%d",
			len(diff.Renamed), len(diff.Updated), len(diff.Added), len(diff.Removed), len(diff.Moved))
	}

	got, err := os.ReadFile(filepath.Join(repoDir, "movies", "Old.json"))
	if err != nil {
		t.Fatalf("file vanished: %v", err)
	}
	if string(got) != string(originalContent) {
		t.Errorf("file was rewritten despite skip:\nbefore: %s\nafter:  %s", originalContent, got)
	}

	reloaded, err := LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	entry, ok := reloaded.Lookup(1, 42)
	if !ok {
		t.Fatal("state entry vanished")
	}
	if entry.LastName != "Old" {
		t.Errorf("state.LastName mutated despite skip: got %q want %q", entry.LastName, "Old")
	}

	// Second pass with Include containing the key — the rename should now
	// surface (proves it wasn't lost permanently).
	diff2, err := RunExport(context.Background(), cfg, client, ExportOptions{
		Include: map[string]bool{"1:42": true},
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("RunExport (preview): %v", err)
	}
	if len(diff2.Renamed) != 1 {
		t.Fatalf("expected rename to resurface in preview, got %d renames", len(diff2.Renamed))
	}
	if diff2.Renamed[0].OldName != "Old" || diff2.Renamed[0].Name != "Old (HD)" {
		t.Errorf("rename diff malformed: %+v", diff2.Renamed[0])
	}
}

// TestRunExportIncludeFilterDoesNotArchiveDeselectedRemovals covers the
// removal-scan branch: a rule has disappeared from Qui (so qui-sync would
// archive its file), but the user deselects it in the preview. The
// archive flow must NOT run; the file stays where it was so the user
// can revisit on the next preview.
func TestRunExportIncludeFilterDoesNotArchiveDeselectedRemovals(t *testing.T) {
	srv := fakeQuiServer(t, map[int][]Automation{1: {}})
	t.Cleanup(srv.Close)

	cfg, repoDir := setupExportFixture(t, srv.URL, "Gone")

	if err := os.WriteFile(filepath.Join(repoDir, "movies", "Gone.json"),
		[]byte(`{"_slug":"old","name":"Gone"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	client := NewQuiClient(srv.URL, "test")

	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{
		Include: map[string]bool{},
	})
	if err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("expected removal to be skipped, got %d", len(diff.Removed))
	}

	if _, err := os.Stat(filepath.Join(repoDir, "movies", "Gone.json")); err != nil {
		t.Errorf("file was archived despite skip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "archive", "movies", "Gone.json")); err == nil {
		t.Errorf("file landed in archive/ despite skip")
	}

	reloaded, err := LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if _, ok := reloaded.Lookup(1, 42); !ok {
		t.Error("state entry forgotten despite skip — next preview will not re-detect the removal")
	}
}

// TestRunExportCommentsAttachToDiffEntries confirms per-rule comments
// flow through RunExport and land on the corresponding DiffEntry. The
// commit-message + changelog rendering tests exercise the downstream
// hand-off, but this one locks down the boundary.
func TestRunExportCommentsAttachToDiffEntries(t *testing.T) {
	srv := fakeQuiServer(t, map[int][]Automation{
		1: {newAutomation(42, 1, "Old (HD)")},
	})
	t.Cleanup(srv.Close)

	cfg, _ := setupExportFixture(t, srv.URL, "Old")
	client := NewQuiClient(srv.URL, "test")

	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{
		DryRun:   true,
		Include:  map[string]bool{"1:42": true},
		Comments: map[string]string{"1:42": "added HD suffix"},
	})
	if err != nil {
		t.Fatalf("RunExport: %v", err)
	}
	if len(diff.Renamed) != 1 {
		t.Fatalf("expected 1 rename, got %d", len(diff.Renamed))
	}
	if diff.Renamed[0].Comment != "added HD suffix" {
		t.Errorf("Comment not propagated: got %q", diff.Renamed[0].Comment)
	}
	if diff.Renamed[0].InstanceID != 1 {
		t.Errorf("InstanceID not stamped: got %d want 1", diff.Renamed[0].InstanceID)
	}
}
