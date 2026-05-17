package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ExportDiff summarises what an export would change.
type ExportDiff struct {
	Added   []DiffEntry
	Updated []DiffEntry
	Removed []DiffEntry
	Renamed []DiffEntry // same qui_id, different name (rule-level rename)
	Moved   []DiffEntry // same qui_id, different category (config change)
}

type DiffEntry struct {
	Slug          string
	Category      string
	Name          string
	OldName       string // for Renamed entries
	OldCategory   string // for Moved entries
	QuiID         int
	InstanceID    int      `json:"instance_id"`               // Qui instance this rule belongs to
	ChangedFields []string `json:"changed_fields,omitempty"`  // for Updated: which top-level JSON keys differ
	Comment       string   `json:"comment,omitempty"`         // optional per-change rationale from the user
}

// Empty returns true if nothing changed.
func (d *ExportDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Updated) == 0 && len(d.Removed) == 0 &&
		len(d.Renamed) == 0 && len(d.Moved) == 0
}

// ExportOptions controls a RunExport invocation. Zero-value is a normal
// full export with no note and no per-rule overrides.
type ExportOptions struct {
	// DryRun: no filesystem changes when true.
	DryRun bool

	// Note: free-form markdown attached to today's CHANGELOG entry.
	Note string

	// Include filters which rules are written this run. Keys are
	// "<instanceID>:<quiID>" — the same shape DiffEntry.RuleKey produces.
	// nil = include everything (default). An explicit empty map means
	// "exclude everything" — a valid request that turns into a no-op export.
	Include map[string]bool

	// Comments carries per-rule rationale text supplied by the user.
	// Keys match Include. Empty / missing keys mean "no comment".
	Comments map[string]string
}

// RuleKey returns the stable identifier for a rule across the
// Include / Comments maps. Centralised so frontend and backend cannot
// drift on the format.
func (e DiffEntry) RuleKey() string {
	return ruleKey(e.InstanceID, e.QuiID)
}

func ruleKey(instanceID, quiID int) string {
	return fmt.Sprintf("%d:%d", instanceID, quiID)
}

// isIncluded reports whether a (instance, rule) pair should be processed
// this run. A nil Include map means "everything included".
func (o ExportOptions) isIncluded(instanceID, quiID int) bool {
	if o.Include == nil {
		return true
	}
	return o.Include[ruleKey(instanceID, quiID)]
}

// RunExport performs an end-to-end export:
//  1. Fetch all automations from configured Qui instances
//  2. For each, assign/reuse a slug
//  3. Transform payload (strip user-specific fields, apply reverse mappings)
//  4. Compare against on-disk rules → compute diff
//  5. Backup removed/changed files
//  6. Write new/updated files
//  7. Update state + changelog
//
// When dryRun is true, no filesystem changes are made.
//
// Atomicity: state is saved after each instance completes, so a mid-export
// crash leaves the already-processed instances in a consistent state. The
// changelog is appended only at the very end, so a partial run does not
// produce a half-written changelog entry.
//
// opts.Include filters which rules are processed; rules NOT in the
// include set are left untouched (no file write, no state update, no
// changelog entry). They will appear again on the next Preview so the
// user can park changes mid-batch without losing them.
func RunExport(ctx context.Context, cfg *Config, client *QuiClient, opts ExportOptions) (*ExportDiff, error) {
	dryRun := opts.DryRun
	paths := cfg.Paths()

	// Serialize concurrent exports on the same repo. Different repos run parallel.
	lock := lockForRepo(paths.Repo)
	lock.Lock()
	defer lock.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	state, err := LoadMaintainerState(paths.State)
	if err != nil {
		return nil, err
	}

	diff := &ExportDiff{}
	// Track which (instance, ruleID) we saw this run so we can detect removals.
	seen := map[int]map[int]bool{}

	for _, inst := range cfg.ExportInstances {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Whole-instance exclude: skip entirely. Leaves existing exported
		// files as orphans (same semantics as per-rule exclude).
		if state.IsInstanceExcluded(inst.QuiInstanceID) {
			continue
		}
		seen[inst.QuiInstanceID] = map[int]bool{}
		stateDirty := false

		rules, err := client.ListAutomations(ctx, inst.QuiInstanceID)
		if err != nil {
			return nil, fmt.Errorf("list automations (instance %d): %w", inst.QuiInstanceID, err)
		}

		for _, r := range rules {
			// Excluded rules are invisible to qui-sync: skip entirely, don't
			// add to seen (so removal detection also skips them).
			if state.IsExcluded(inst.QuiInstanceID, r.ID) {
				continue
			}
			// Per-rule include filter (user deselected this row in the
			// Publish preview). Mark as seen so the removal scan doesn't
			// archive the file — the rule is still in Qui, the user just
			// chose not to push the change this round.
			if !opts.isIncluded(inst.QuiInstanceID, r.ID) {
				seen[inst.QuiInstanceID][r.ID] = true
				continue
			}
			seen[inst.QuiInstanceID][r.ID] = true

			ruleComment := opts.Comments[ruleKey(inst.QuiInstanceID, r.ID)]

			// Resolve slug: reuse from state, else assign a new unique one.
			entry, ok := state.Lookup(inst.QuiInstanceID, r.ID)
			var slug string
			var oldCategory string
			if ok {
				slug = entry.Slug
				oldCategory = entry.Category
				if entry.LastName != r.Name {
					diff.Renamed = append(diff.Renamed, DiffEntry{
						Slug: slug, Category: inst.Category,
						Name: r.Name, OldName: entry.LastName, QuiID: r.ID,
						InstanceID: inst.QuiInstanceID, Comment: ruleComment,
					})
				}
				// Category-move: state says category X, config now says Y.
				// Treat as a move: remove old file, write new, log in diff.
				if oldCategory != "" && oldCategory != inst.Category {
					diff.Moved = append(diff.Moved, DiffEntry{
						Slug: slug, Category: inst.Category, OldCategory: oldCategory,
						Name: r.Name, QuiID: r.ID,
						InstanceID: inst.QuiInstanceID, Comment: ruleComment,
					})
				}
			} else {
				taken := state.AllSlugsInInstance(inst.QuiInstanceID)
				slug = UniqueSlug(r.Name, taken)
			}

			// Effective sortOrder = user override (if any) else Qui's own value.
			effectiveSortOrder := r.SortOrder
			if override, ok := state.SortOrderOverride(inst.QuiInstanceID, r.ID); ok {
				effectiveSortOrder = override
			}

			// Transform the raw payload into the upstream file shape.
			fileJSON, err := buildRuleFile(r, slug, cfg, effectiveSortOrder)
			if err != nil {
				return nil, fmt.Errorf("build rule file (id=%d): %w", r.ID, err)
			}

			path := filepath.Join(paths.Repo, inst.Category, RuleFilename(r.Name)+".json")
			renamed := entry != nil && entry.LastName != r.Name
			oldPath := ""
			if oldCategory != "" && oldCategory != inst.Category {
				oldPath = filepath.Join(paths.Repo, oldCategory, RuleFilename(r.Name)+".json")
			} else if renamed && RuleFilename(entry.LastName) != RuleFilename(r.Name) {
				// Pure rename within the same category — the old filename
				// is now orphaned on disk. Track it so the backup+remove
				// flow below cleans it up the same way it cleans up the
				// old-category file on a move. Without this, both the old
				// and new filenames end up committed to the share-repo and
				// pushed to GitHub on the next push.
				//
				// Filename equality check avoids a self-destructive remove
				// when the rename only touches characters that RuleFilename
				// strips (e.g. "Foo: Bar" → "Foo / Bar" both yield
				// "Foo Bar.json"); in that case there is no orphan.
				oldPath = filepath.Join(paths.Repo, inst.Category, RuleFilename(entry.LastName)+".json")
			}

			existing, existsErr := os.ReadFile(path)
			exists := existsErr == nil
			if exists && oldPath == "" {
				// Compare semantic JSON (ignore whitespace).
				if jsonEqual(existing, fileJSON) {
					// No functional changes. Still backfill state in case it was missing.
					if entry == nil || entry.LastName != r.Name || entry.Category != inst.Category {
						state.Assign(inst.QuiInstanceID, r.ID, slug, r.Name, inst.Category)
						stateDirty = true
					}
					continue
				}
				// If only the `name` field differs and the rule was renamed,
				// treat it as a pure rename — already logged above, skip Updated.
				// Otherwise it's a real content change.
				if !(renamed && jsonEqualExceptName(existing, fileJSON)) {
					diff.Updated = append(diff.Updated, DiffEntry{
						Slug: slug, Category: inst.Category, Name: r.Name, QuiID: r.ID,
						InstanceID:    inst.QuiInstanceID,
						Comment:       ruleComment,
						ChangedFields: computeChangedFields(existing, fileJSON),
					})
				}
			} else if !exists && oldPath == "" {
				diff.Added = append(diff.Added, DiffEntry{
					Slug: slug, Category: inst.Category, Name: r.Name, QuiID: r.ID,
					InstanceID: inst.QuiInstanceID, Comment: ruleComment,
				})
			}
			// If oldPath != "", the move entry is already logged above; we skip
			// the Updated/Added classification since it's a move.

			if !dryRun {
				// Backup old file if we're about to overwrite or move it.
				if exists {
					if err := backupFile(path, paths.Backups); err != nil {
						return nil, err
					}
				}
				if oldPath != "" {
					if _, err := os.Stat(oldPath); err == nil {
						if err := backupFile(oldPath, paths.Backups); err != nil {
							return nil, err
						}
						if err := os.Remove(oldPath); err != nil {
							return nil, fmt.Errorf("remove stale rule file %s: %w", oldPath, err)
						}
					}
				}
				// If the rule was previously archived (excluded → un-excluded,
				// or removed-from-Qui → returned), clean up the archive copy.
				// Without this, the rule would appear in BOTH movies/ (active)
				// AND archive/movies/ (orphan from the prior exclude). The
				// archive lookup uses entry.LastName + entry.Category so a
				// rule renamed-while-archived still gets its old archive
				// entry cleaned up correctly.
				if entry != nil && entry.LastName != "" {
					archiveCategory := inst.Category
					if entry.Category != "" {
						archiveCategory = entry.Category
					}
					archivePath := filepath.Join(paths.Repo, "archive", archiveCategory, RuleFilename(entry.LastName)+".json")
					if _, err := os.Stat(archivePath); err == nil {
						if err := os.Remove(archivePath); err != nil {
							return nil, fmt.Errorf("remove stale archive on re-activation: %w", err)
						}
					}
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, fileJSON, 0o644); err != nil {
					return nil, fmt.Errorf("write %s: %w", path, err)
				}
			}
			state.Assign(inst.QuiInstanceID, r.ID, slug, r.Name, inst.Category)
			stateDirty = true
		}

		// Per-instance state save: limits blast radius if a later instance fails.
		if !dryRun && stateDirty {
			if err := state.Save(paths.State); err != nil {
				return nil, fmt.Errorf("save state after instance %d: %w", inst.QuiInstanceID, err)
			}
		}
	}

	// Detect removals: anything in state for a scanned instance that wasn't seen.
	for _, inst := range cfg.ExportInstances {
		// Instance-level excludes skip the removal scan too.
		if state.IsInstanceExcluded(inst.QuiInstanceID) {
			continue
		}
		seenInstance, ok := seen[inst.QuiInstanceID]
		if !ok {
			continue
		}
		removalsDirty := false
		for _, quiID := range state.SortedRuleIDs(inst.QuiInstanceID) {
			if seenInstance[quiID] {
				continue
			}
			entry, _ := state.Lookup(inst.QuiInstanceID, quiID)
			if entry == nil {
				continue
			}
			// Only mark removals for rules whose recorded category matches the
			// instance we just scanned, to avoid clashes across categories.
			if entry.Category != "" && entry.Category != inst.Category {
				continue
			}
			// Per-rule include filter: if the user deselected this archive
			// in the preview, skip the archive flow this round. State stays,
			// file stays, removal will show up again on the next Preview.
			if !opts.isIncluded(inst.QuiInstanceID, quiID) {
				continue
			}
			// Two reasons for a state entry not to be in `seen` this run:
			//   1. The rule was deleted from Qui itself (gone forever)
			//   2. The user excluded it via qui-sync's checkbox UI
			// Both paths must archive the file from the active repo — otherwise
			// previously-pushed excluded rules stay on GitHub forever and the
			// user's exclusion has no public-facing effect. Difference is in
			// state handling: deleted-from-Qui forgets the state entry; excluded
			// keeps it so the slug + last-known-name survive an un-exclude.
			excluded := state.IsExcluded(inst.QuiInstanceID, quiID)
			path := filepath.Join(paths.Repo, entry.Category, RuleFilename(entry.LastName)+".json")
			fileExists := false
			if _, err := os.Stat(path); err == nil {
				fileExists = true
			}
			// Only flag as Removed (and run the archive flow) when there's
			// actually a file to remove. After a Reset to Remote the local
			// repo can be wiped while state still has entries — without this
			// guard, Preview would report "N archived" with no real change.
			// For deleted-from-Qui rules with no local file, we still need
			// to forget state so it doesn't accumulate stale entries.
			if !fileExists {
				if !excluded && !dryRun {
					state.Forget(inst.QuiInstanceID, quiID)
					removalsDirty = true
				}
				continue
			}
			diff.Removed = append(diff.Removed, DiffEntry{
				Slug: entry.Slug, Category: entry.Category, Name: entry.LastName, QuiID: quiID,
				InstanceID: inst.QuiInstanceID,
				Comment:    opts.Comments[ruleKey(inst.QuiInstanceID, quiID)],
			})
			if !dryRun {
				// path + fileExists already computed above. Re-confirm the
				// stat — race-safe in case the file disappeared between
				// the check and now (rare, but cheap).
				if _, err := os.Stat(path); err == nil {
					// Local timestamped backup in /data/backups — not
					// pushed to GitHub, safety net against UI mistakes.
					if err := backupFile(path, paths.Backups); err != nil {
						return nil, err
					}
					// Archive to /data/repo/archive/<category>/<name>.json —
					// kept under version control so the rule's history
					// stays visible on GitHub.
					//
					// If a rule with the same name has already been
					// archived before (rare — user-confirmed "probably
					// not, archived rules don't come back"), rename
					// the existing archive with a timestamp suffix so
					// the previous version is not silently destroyed.
					// git history on the remote still holds earlier
					// committed versions, but this protects the case
					// where the first archive was never pushed.
					archivePath := filepath.Join(paths.Repo, "archive", entry.Category, RuleFilename(entry.LastName)+".json")
					if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
						return nil, fmt.Errorf("mkdir archive dir: %w", err)
					}
					if _, err := os.Stat(archivePath); err == nil {
						stamped := archivePath[:len(archivePath)-len(".json")] + "_" + time.Now().Format("2006-01-02") + ".json"
						if err := os.Rename(archivePath, stamped); err != nil {
							return nil, fmt.Errorf("preserve prior archive %s: %w", archivePath, err)
						}
					}
					if err := os.Rename(path, archivePath); err != nil {
						return nil, fmt.Errorf("archive %s: %w", path, err)
					}
				}
				// Only drop the state entry when the rule is gone from Qui.
				// For excluded rules, keep the entry so slug + last-known-
				// name are preserved for a potential un-exclude later.
				if !excluded {
					state.Forget(inst.QuiInstanceID, quiID)
				}
				removalsDirty = true
			}
		}
		if !dryRun && removalsDirty {
			if err := state.Save(paths.State); err != nil {
				return nil, fmt.Errorf("save state after removals (instance %d): %w", inst.QuiInstanceID, err)
			}
		}
	}

	if !dryRun && !diff.Empty() {
		if err := AppendChangelog(paths.Repo, diff, time.Now(), opts.Note); err != nil {
			return nil, err
		}
	}

	// Deterministic order in reports.
	sortDiffSlices(diff)

	return diff, nil
}

// ruleFileOut is the on-disk shape of an exported rule. Fields are laid
// out in a struct (not a map) so Go's JSON encoder emits them in the
// order declared here.
//
// Layout choice: Qui's own export endpoint produces
//
//	name, trackerPattern, trackerDomains, conditions, intervalSeconds
//
// (no sortOrder in that payload). We preserve that exact sequence for
// the Qui-native fields, then bookend with qui-sync metadata:
//
//	_slug, _description                                  ← qui-sync prefix
//	name, trackerPattern, trackerDomains,
//	conditions, intervalSeconds                          ← Qui's order, verbatim
//	sortOrder                                            ← qui-sync suffix
//
// sortOrder lands at the end because it is a qui-sync-managed value
// (either Qui's own value or a maintainer override from state.json) —
// keeping it after intervalSeconds means Qui's slice of the file is
// byte-identical to its native export format.
//
// "conditions" stays as json.RawMessage so Qui's internal ordering
// inside that block (schemaVersion first, action sub-blocks with
// enabled/mode before the condition predicate, operator before the
// conditions array) survives the round-trip verbatim.
type ruleFileOut struct {
	Slug            string          `json:"_slug,omitempty"`
	Description     string          `json:"_description,omitempty"`
	Name            string          `json:"name"`
	TrackerPattern  string          `json:"trackerPattern"`
	TrackerDomains  []string        `json:"trackerDomains"`
	Conditions      json.RawMessage `json:"conditions,omitempty"`
	IntervalSeconds int             `json:"intervalSeconds,omitempty"`
	SortOrder       int             `json:"sortOrder"`
}

// buildRuleFile converts a Qui Automation into the upstream file
// representation. Fields are emitted in the community-convention
// reading order (identity → trackers → conditions → interval); the
// conditions block is passed through byte-for-byte from Qui so the
// semantic ordering Qui uses inside it (schemaVersion first, action
// enabled/mode before the condition predicate, etc.) is preserved.
//
// Strips identity + user-owned fields (id, enabled, dryRun, ...) from
// the input before emission. Replaces personal tracker values with
// placeholders so trackers are never published. Wildcard patterns ("*")
// are preserved as-is since they are not personal data.
//
// TODO(v0.2): apply cfg.ReverseMappings to tags/categories/paths inside
// conditions. Not implemented yet — conditions pass through untouched.
func buildRuleFile(r Automation, slug string, cfg *Config, sortOrder int) ([]byte, error) {
	// Decode only the fields we ship to disk. Stripped fields (id,
	// enabled, notify, ...) are never read — they cannot leak even if
	// Qui starts returning new fields we are not aware of.
	var src struct {
		Name            string          `json:"name"`
		TrackerPattern  string          `json:"trackerPattern"`
		TrackerDomains  []string        `json:"trackerDomains"`
		Conditions      json.RawMessage `json:"conditions"`
		IntervalSeconds int             `json:"intervalSeconds"`
		Description     string          `json:"_description"`
	}
	if err := json.Unmarshal(r.Raw, &src); err != nil {
		return nil, err
	}

	// Replace real tracker values with placeholders for sharing.
	// Trackers with "*" (all) are kept as-is — they're not personal.
	if src.TrackerPattern != "" && src.TrackerPattern != "*" {
		src.TrackerPattern = "tracker_1"
	}
	if len(src.TrackerDomains) > 0 {
		isWildcard := len(src.TrackerDomains) == 1 && src.TrackerDomains[0] == "*"
		if !isWildcard {
			src.TrackerDomains = []string{"tracker.xyz"}
		}
	}

	out := ruleFileOut{
		Slug:            slug,
		Description:     src.Description,
		Name:            src.Name,
		TrackerPattern:  src.TrackerPattern,
		TrackerDomains:  src.TrackerDomains,
		Conditions:      src.Conditions,
		IntervalSeconds: src.IntervalSeconds,
		SortOrder:       sortOrder,
	}

	// cfg.ReverseMappings is accepted but unused in v0.1 — see TODO above.
	_ = cfg.ReverseMappings

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// jsonEqualExceptName compares two JSON byte slices for semantic equality after
// removing the top-level "name" field from both. Used to detect pure renames
// (where only the name changed) so they're not double-reported as Updated.
func jsonEqualExceptName(a, b []byte) bool {
	stripName := func(in []byte) []byte {
		var obj map[string]any
		if err := json.Unmarshal(in, &obj); err != nil {
			return in
		}
		delete(obj, "name")
		out, _ := json.Marshal(obj)
		return out
	}
	return jsonEqual(stripName(a), stripName(b))
}

// jsonEqual compares two JSON byte slices for semantic equality (ignores whitespace).
func jsonEqual(a, b []byte) bool {
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return deepEqualJSON(av, bv)
}

func deepEqualJSON(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !deepEqualJSON(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !deepEqualJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// backupFile moves a file into <repo>/backup/ with a date suffix.
// If multiple runs on the same day, appends -1, -2, etc.
func backupFile(src, backupsDir string) error {
	backupDir := backupsDir
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(src)
	slug := strings.TrimSuffix(base, filepath.Ext(base))
	ext := filepath.Ext(base)
	date := time.Now().Format("2006-01-02")
	dst := filepath.Join(backupDir, fmt.Sprintf("%s-%s%s", slug, date, ext))
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(backupDir, fmt.Sprintf("%s-%s-%d%s", slug, date, i, ext))
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// CleanBackups deletes backup files older than retentionDays.
func CleanBackups(backupsDir string, retentionDays int) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	backupDir := backupsDir
	entries, err := os.ReadDir(backupDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(backupDir, e.Name())); err == nil {
				count++
			}
		}
	}
	return count, nil
}

// computeChangedFields returns which top-level JSON keys differ between
// two rule files. Used to give the user context on what changed.
func computeChangedFields(oldJSON, newJSON []byte) []string {
	var oldMap, newMap map[string]any
	if json.Unmarshal(oldJSON, &oldMap) != nil || json.Unmarshal(newJSON, &newMap) != nil {
		return nil
	}
	// Check all keys in both maps.
	allKeys := map[string]bool{}
	for k := range oldMap {
		allKeys[k] = true
	}
	for k := range newMap {
		allKeys[k] = true
	}
	var changed []string
	for k := range allKeys {
		if k == "_slug" || k == "_description" {
			continue // internal fields, not interesting to user
		}
		oldVal, oldOk := oldMap[k]
		newVal, newOk := newMap[k]
		if !oldOk && newOk {
			changed = append(changed, "+"+k)
		} else if oldOk && !newOk {
			changed = append(changed, "-"+k)
		} else if !deepEqualJSON(oldVal, newVal) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	return changed
}

func sortDiffSlices(d *ExportDiff) {
	sortByCategorySlug := func(s []DiffEntry) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].Category != s[j].Category {
				return s[i].Category < s[j].Category
			}
			return s[i].Slug < s[j].Slug
		})
	}
	sortByCategorySlug(d.Added)
	sortByCategorySlug(d.Updated)
	sortByCategorySlug(d.Removed)
	sortByCategorySlug(d.Renamed)
	sortByCategorySlug(d.Moved)
}
