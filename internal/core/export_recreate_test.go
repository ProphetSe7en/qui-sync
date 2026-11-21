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

// quiStub serves one instance's automation list and lets a test swap it
// between exports.
type quiStub struct {
	srv  *httptest.Server
	body string
}

func newQuiStub(t *testing.T) *quiStub {
	t.Helper()
	s := &quiStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/instances/1/automations" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, s.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func unregisteredRule(id, interval int) string {
	return fmt.Sprintf(`[{"id":%d,"instanceId":1,"name":"Delete: unregistered torrents",
		"enabled":true,"sortOrder":3,"intervalSeconds":%d,
		"conditions":{"schemaVersion":"1","delete":{"enabled":true,"mode":"deleteWithFiles"}}}]`, id, interval)
}

func fileSlug(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f struct {
		Slug string `json:"_slug"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f.Slug
}

// TestExportRuleRecreatedUnderSameName covers a rule deleted in Qui and
// created again with the same name. Qui issues a new rule id, so the two
// halves of the export used to disagree: the rule looked new (and got a
// "-2" slug because the old entry still reserved the name) while the old
// id looked removed. Both resolve to the same file, since a rule's file
// is named after the rule, so the archive step moved the active file out
// of the repo and left the rule with no file at all.
func TestExportRuleRecreatedUnderSameName(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	active := filepath.Join(cfg.RepoDir, "series", "Delete unregistered torrents.json")
	archived := filepath.Join(cfg.RepoDir, "archive", "series", "Delete unregistered torrents.json")

	qui.body = unregisteredRule(10, 300)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if got := fileSlug(t, active); got != "delete-unregistered-torrents" {
		t.Fatalf("first export slug = %q, want delete-unregistered-torrents", got)
	}

	// Deleted and recreated in Qui, with one field changed.
	qui.body = unregisteredRule(11, 600)
	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}

	if got := fileSlug(t, active); got != "delete-unregistered-torrents" {
		t.Errorf("active file slug = %q, want the original delete-unregistered-torrents", got)
	}
	if _, err := os.Stat(archived); !os.IsNotExist(err) {
		t.Errorf("recreated rule must not be archived, but %s exists", archived)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("Removed = %+v, want none: the rule is still in Qui", diff.Removed)
	}
	if len(diff.Added) != 0 {
		t.Errorf("Added = %+v, want none: the rule keeps its identity", diff.Added)
	}
	if len(diff.Updated) != 1 || diff.Updated[0].Slug != "delete-unregistered-torrents" {
		t.Errorf("Updated = %+v, want one entry on the original slug", diff.Updated)
	}

	// A second run has nothing left to do.
	diff2, err := RunExport(context.Background(), cfg, client, ExportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("third export: %v", err)
	}
	if !diff2.Empty() {
		t.Errorf("follow-up preview should be empty, got %+v", diff2)
	}
}

// TestExportRepairsAlreadyBrokenRecreate starts from the state the old
// behaviour left behind (slug "-2" in state, the file sitting in archive/,
// nothing active) and checks what a user gets once they update: the rule
// comes back as an active file and the stale archive copy is cleaned up.
// The slug stays "-2" because it is the published identity of the rule by
// then; renaming it is a user decision, not something an export should do
// behind their back.
func TestExportRepairsAlreadyBrokenRecreate(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	paths := cfg.Paths()
	active := filepath.Join(cfg.RepoDir, "series", "Delete unregistered torrents.json")
	archived := filepath.Join(cfg.RepoDir, "archive", "series", "Delete unregistered torrents.json")

	// Rebuild the damaged state: rule 11 known under the "-2" slug, rule
	// 10 forgotten, and the file in archive/ instead of series/.
	state, err := LoadMaintainerState(paths.State)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	state.Assign(1, 11, "delete-unregistered-torrents-2", "Delete: unregistered torrents", "series")
	if err := state.Save(paths.State); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(archived), 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(archived, []byte(`{"_slug":"delete-unregistered-torrents-2"}`), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	qui.body = unregisteredRule(11, 600)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}

	if got := fileSlug(t, active); got != "delete-unregistered-torrents-2" {
		t.Errorf("active file slug = %q, want delete-unregistered-torrents-2 restored", got)
	}
	if _, err := os.Stat(archived); !os.IsNotExist(err) {
		t.Errorf("stale archive copy should be removed, but %s still exists", archived)
	}
}

// TestExportRecreatedIdenticalRuleStaysTracked covers the recreate that
// changes nothing: the export has no file to write, so the rule must still
// be recorded under its new id. An untracked rule would look new again on
// every later export.
func TestExportRecreatedIdenticalRuleStaysTracked(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	paths := cfg.Paths()

	qui.body = unregisteredRule(10, 300)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// Recreated with identical content, so nothing is written this run.
	qui.body = unregisteredRule(11, 300)
	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if !diff.Empty() {
		t.Errorf("an identical recreate should produce no changes, got %+v", diff)
	}

	state, err := LoadMaintainerState(paths.State)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	entry, ok := state.Lookup(1, 11)
	if !ok {
		t.Fatalf("rule 11 is missing from the saved state")
	}
	if entry.Slug != "delete-unregistered-torrents" {
		t.Errorf("slug = %q, want delete-unregistered-torrents", entry.Slug)
	}
	if _, stale := state.Lookup(1, 10); stale {
		t.Errorf("rule 10 no longer exists in Qui and should be gone from state")
	}
}

// TestExportRecreatedRuleKeepsSortOrderOverride checks that the manual
// sort order a maintainer set on a rule survives the rule being made
// again in Qui. The override is stored per rule id, so a re-key that
// moves only the slug would drop it and republish Qui's own value.
func TestExportRecreatedRuleKeepsSortOrderOverride(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	paths := cfg.Paths()
	active := filepath.Join(cfg.RepoDir, "series", "Delete unregistered torrents.json")

	qui.body = unregisteredRule(10, 300)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// The maintainer pins the rule's position in the published repo.
	state, err := LoadMaintainerState(paths.State)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	state.SetSortOrderOverride(1, 10, 99)
	if err := state.Save(paths.State); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("export with override: %v", err)
	}

	qui.body = unregisteredRule(11, 300)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("export after recreate: %v", err)
	}

	b, err := os.ReadFile(active)
	if err != nil {
		t.Fatalf("read active file: %v", err)
	}
	var f struct {
		SortOrder int `json:"sortOrder"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse active file: %v", err)
	}
	if f.SortOrder != 99 {
		t.Errorf("sortOrder = %d, want the pinned 99", f.SortOrder)
	}

	state, err = LoadMaintainerState(paths.State)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if _, ok := state.SortOrderOverride(1, 11); !ok {
		t.Errorf("the override should follow the rule to its new id")
	}
	if _, stale := state.SortOrderOverride(1, 10); stale {
		t.Errorf("the override on the dead id should be gone")
	}
}

// TestExportRuleRenamedOntoAnotherRulesFileReportsRemoval covers the
// case the claimed-path guard must not hide: a live rule renamed onto a
// deleted rule's file. The file stays, because a rule that still exists
// owns it, but the rule whose published file is gone has to appear in
// the diff rather than disappearing quietly.
func TestExportRuleRenamedOntoAnotherRulesFileReportsRemoval(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	foo := filepath.Join(cfg.RepoDir, "series", "Foo.json")
	archivedFoo := filepath.Join(cfg.RepoDir, "archive", "series", "Foo.json")

	qui.body = `[{"id":10,"instanceId":1,"name":"Foo","enabled":true,"sortOrder":1,"intervalSeconds":300},
		{"id":11,"instanceId":1,"name":"Bar","enabled":true,"sortOrder":2,"intervalSeconds":300}]`
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// Foo is deleted in Qui and Bar is renamed to Foo in the same window.
	qui.body = `[{"id":11,"instanceId":1,"name":"Foo","enabled":true,"sortOrder":2,"intervalSeconds":300}]`
	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}

	if _, err := os.Stat(foo); err != nil {
		t.Errorf("the surviving rule's file must stay in place: %v", err)
	}
	if _, err := os.Stat(archivedFoo); !os.IsNotExist(err) {
		t.Errorf("a file a live rule owns must not be archived")
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Slug != "foo" {
		t.Errorf("Removed = %+v, want the rule whose published file is gone", diff.Removed)
	}
	if got := fileSlug(t, foo); got != "bar" {
		t.Errorf("file slug = %q, want bar, the rule that still exists", got)
	}
}

// TestExportStaleIncludeListDoesNotArchiveLiveRule covers the Commit
// path: the include list is built from the previous preview, so a rule
// recreated since then can be filtered out of the run while the dead
// entry pointing at its file is still included. The live rule's file
// must survive, and nothing is reported, because nothing happened to it.
func TestExportStaleIncludeListDoesNotArchiveLiveRule(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	active := filepath.Join(cfg.RepoDir, "series", "Delete unregistered torrents.json")
	archived := filepath.Join(cfg.RepoDir, "archive", "series", "Delete unregistered torrents.json")

	qui.body = unregisteredRule(10, 300)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// Recreated in Qui after the preview was taken, so the include list
	// still names the old id and not the new one.
	qui.body = unregisteredRule(11, 600)
	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{
		Include: map[string]bool{ruleKey(1, 10): true},
	})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}

	if _, err := os.Stat(active); err != nil {
		t.Errorf("the live rule's file was archived behind a stale include list: %v", err)
	}
	if _, err := os.Stat(archived); !os.IsNotExist(err) {
		t.Errorf("nothing should have been archived, but %s exists", archived)
	}
	if len(diff.Removed) != 0 {
		t.Errorf("Removed = %+v, want none: the file is untouched", diff.Removed)
	}
}

// TestExportFieldChangesCarryValues checks the preview can say what a
// field was and what it became, which is the whole point of showing a
// change at all. A nested block gets its name only: naming it is
// honest, summarising a condition tree in one line is not.
func TestExportFieldChangesCarryValues(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")

	qui.body = `[{"id":10,"instanceId":1,"name":"Tag: Season Pack Exists","enabled":true,"sortOrder":3,
		"intervalSeconds":300,"conditions":{"schemaVersion":"1","tag":{"enabled":true,"mode":"full"}}}]`
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}

	qui.body = `[{"id":10,"instanceId":1,"name":"Tag: Season Pack Exists","enabled":true,"sortOrder":5,
		"intervalSeconds":600,"conditions":{"schemaVersion":"1","tag":{"enabled":true,"mode":"partial"}}}]`
	diff, err := RunExport(context.Background(), cfg, client, ExportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if len(diff.Updated) != 1 {
		t.Fatalf("Updated = %+v, want one entry", diff.Updated)
	}

	got := map[string]FieldChange{}
	for _, c := range diff.Updated[0].FieldChanges {
		got[c.Field] = c
	}
	if c := got["sortOrder"]; c.Old != "3" || c.New != "5" {
		t.Errorf("sortOrder change = %+v, want 3 to 5", c)
	}
	if c := got["intervalSeconds"]; c.Old != "300" || c.New != "600" {
		t.Errorf("intervalSeconds change = %+v, want 300 to 600", c)
	}
	c, ok := got["conditions"]
	if !ok {
		t.Fatalf("conditions should be listed as changed, got %+v", diff.Updated[0].FieldChanges)
	}
	if c.Old != "" || c.New != "" {
		t.Errorf("conditions should be named without values, got %+v", c)
	}

	// The plain field-name list stays in step with the detailed one.
	if len(diff.Updated[0].ChangedFields) != len(diff.Updated[0].FieldChanges) {
		t.Errorf("changed fields %v and field changes %+v disagree",
			diff.Updated[0].ChangedFields, diff.Updated[0].FieldChanges)
	}
}

func TestDisplayValue(t *testing.T) {
	long := strings.Repeat("x", displayValueMaxLen+1)
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"number", float64(600), "600"},
		{"fraction", 1.5, "1.5"},
		{"bool", true, "true"},
		{"short string", "tracker_1", "tracker_1"},
		{"empty string", "", "empty"},
		{"long string", long, ""},
		{"string list", []any{"a.com", "b.net"}, "a.com, b.net"},
		{"empty list", []any{}, "empty"},
		{"long list", []any{long}, ""},
		{"list of objects", []any{map[string]any{"a": 1}}, ""},
		{"object", map[string]any{"a": 1}, ""},
		{"null", nil, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayValue(tc.in); got != tc.want {
				t.Errorf("displayValue(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExportTwoRulesSharingAName guards the fallback: when a name cannot
// identify one rule, the export must not adopt a slug by guessing, and it
// must still refuse to archive a file a live rule writes.
func TestExportTwoRulesSharingAName(t *testing.T) {
	repo := t.TempDir()
	qui := newQuiStub(t)

	cfg := &Config{
		Mode:            "maintainer",
		RepoDir:         filepath.Join(repo, "repo"),
		ExportInstances: []ExportInstance{{QuiInstanceID: 1, Category: "series"}},
	}
	client := NewQuiClient(qui.srv.URL, "test-key")
	active := filepath.Join(cfg.RepoDir, "series", "Delete unregistered torrents.json")
	archived := filepath.Join(cfg.RepoDir, "archive", "series", "Delete unregistered torrents.json")

	// Two rules with the same name: ids 10 and 11.
	qui.body = `[{"id":10,"instanceId":1,"name":"Delete: unregistered torrents","enabled":true,"sortOrder":1,"intervalSeconds":300},
		{"id":11,"instanceId":1,"name":"Delete: unregistered torrents","enabled":true,"sortOrder":2,"intervalSeconds":300}]`
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("first export: %v", err)
	}

	// Both are deleted and a third rule appears under the same name.
	qui.body = unregisteredRule(12, 900)
	if _, err := RunExport(context.Background(), cfg, client, ExportOptions{}); err != nil {
		t.Fatalf("second export: %v", err)
	}

	if got := fileSlug(t, active); got == "" {
		t.Errorf("the live rule must keep an active file, found none at %s", active)
	}
	if _, err := os.Stat(archived); !os.IsNotExist(err) {
		t.Errorf("the live rule's file must not be archived, but %s exists", archived)
	}
}
