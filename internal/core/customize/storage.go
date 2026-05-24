// Package customize handles per-rule personal customizations on top of
// upstream-subscribed rules. A customization captures the diff between
// the maintainer's published rule and the user's locally-edited Qui
// rule, persists it, and re-applies it on every upstream pull so
// personal touches survive maintainer updates.
//
// See dev/docs/personal-overrides-spec.md for the design — particularly
// the "Three-layer merge architecture" section that frames where this
// package sits in the apply pipeline (Layer 2, between upstream baseline
// and consumer-managed field preservation).
package customize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileFormatVersion is the schema version of the customization file on
// disk. Bump only on incompatible format changes; readers reject any
// version they don't recognise so a future-format file in the state dir
// never silently misapplies on an older qui-sync.
const FileFormatVersion = 1

// CapturedFrom is a provenance flag for telemetry + debug — did this
// customization come from the setup-time auto-detect modal at
// subscription-mapping time, or from a post-setup Detect-changes click?
// Strict enum so changing the source surface (e.g. adding a third path)
// shows up as a compile-time concern, not a silent string drift.
type CapturedFrom string

const (
	CapturedFromSetupTime CapturedFrom = "setup-time"
	CapturedFromPostSetup CapturedFrom = "post-setup-detect"
)

// Customization is the on-disk payload for one rule's personal diff.
// JSON shape exactly matches the spec's "Storage layout" section. New
// fields go at the bottom with omitempty so older files keep loading.
type Customization struct {
	Version              int             `json:"version"`
	SchemaVersionAssumed string          `json:"schema_version_assumed"`
	BaseSHA              string          `json:"base_sha"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	CapturedFrom         CapturedFrom    `json:"captured_from"`
	Diff                 json.RawMessage `json:"diff"`
	Notes                string          `json:"notes,omitempty"`
	// FragileOps lists JSON Pointer paths in the diff whose ops are
	// positional and therefore likely to break if upstream reorders
	// the surrounding array. Empty means every op is robust (append-
	// to-end + scalar replace). Populated by Capture; the UI uses it
	// to warn "this customization contains N positional edits — it
	// may need re-capture if upstream restructures this rule."
	// Per spec "Robustness by operation type" matrix. Review C3.
	FragileOps []string `json:"fragile_ops,omitempty"`
}

// customizationsDir returns <stateDir>/customizations.
// Centralised so callers don't hard-code the layout — if we ever move
// the directory (unlikely; matches consumer.state.json sibling pattern),
// this is the one place to change.
func customizationsDir(stateDir string) string {
	return filepath.Join(stateDir, "customizations")
}

// filePath returns the absolute path for a (subscription, slug) pair.
// Both segments go through validIdentifier — defense in depth even
// though normal callers (core.Slugify on rule names + AddSubscription
// on subscription slugs) already produce safe input. The trust
// boundary matters: rule `_slug` is MAINTAINER-controlled (lives in
// the maintainer's git repo we pulled from), so a hostile maintainer
// or compromised upstream could ship a rule with
// `_slug: "../../tmp/pwned"`. Reject those at the package boundary so
// no caller can accidentally write outside the customizations tree.
func filePath(stateDir, sub, slug string) string {
	return filepath.Join(customizationsDir(stateDir), sub, slug+".json")
}

// validIdentifier reports whether s is safe to use as a file-system
// segment in the customizations layout. Accepts kebab-case plus a
// few defensive characters (digits, dots in the middle). Rejects
// path separators, parent-dir traversal, leading dots, empty input.
func validIdentifier(s string) bool {
	if s == "" {
		return false
	}
	// Length cap: filesystems vary; 200 chars leaves headroom for
	// the .json suffix and the customizations/<sub>/ prefix on
	// even modest path-length limits.
	if len(s) > 200 {
		return false
	}
	// Reject anything containing a separator or escape sequence.
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	// First character must be alnum (rejects ".hidden" and "-flag").
	if s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// ErrInvalidIdentifier signals that a sub or slug failed validation.
// Returned from Load/Save/Delete instead of silently constructing a
// dangerous file path. Callers map this to a 400 (the caller passed
// bad data) rather than 500.
var ErrInvalidIdentifier = fmt.Errorf("invalid subscription or slug identifier (must be kebab-case, no path separators)")

// Load reads the customization for (sub, slug). Returns nil + nil when
// the file doesn't exist — "no customization" is the default state, not
// an error. Returns a wrapped error for parse failures + unknown
// format-version (defensive against forward-incompat files).
func Load(stateDir, sub, slug string) (*Customization, error) {
	if sub == "" || slug == "" {
		return nil, fmt.Errorf("sub and slug are required")
	}
	if !validIdentifier(sub) || !validIdentifier(slug) {
		return nil, ErrInvalidIdentifier
	}
	path := filePath(stateDir, sub, slug)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read customization %s: %w", path, err)
	}
	var c Customization
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse customization %s: %w", path, err)
	}
	if c.Version != FileFormatVersion {
		return nil, fmt.Errorf("customization %s has unsupported version %d (expected %d)", path, c.Version, FileFormatVersion)
	}
	return &c, nil
}

// Save writes the customization atomically (tmp + rename). Creates the
// containing directories on demand. Sets UpdatedAt to now; preserves
// CreatedAt if non-zero, otherwise sets it to UpdatedAt — Capture
// already populates these correctly but Save is defensive in case a
// caller hand-constructs a Customization.
func Save(stateDir, sub, slug string, c *Customization) error {
	if c == nil {
		return fmt.Errorf("customization is nil")
	}
	if sub == "" || slug == "" {
		return fmt.Errorf("sub and slug are required")
	}
	if !validIdentifier(sub) || !validIdentifier(slug) {
		return ErrInvalidIdentifier
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.Version == 0 {
		c.Version = FileFormatVersion
	}
	path := filePath(stateDir, sub, slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal customization: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// Delete removes the customization for (sub, slug). Idempotent —
// missing file is treated as already-deleted (no error). Used by the
// Reset action (toggle Customize OFF or explicit "drop my changes"
// from the conflict modal).
func Delete(stateDir, sub, slug string) error {
	if sub == "" || slug == "" {
		return fmt.Errorf("sub and slug are required")
	}
	if !validIdentifier(sub) || !validIdentifier(slug) {
		return ErrInvalidIdentifier
	}
	path := filePath(stateDir, sub, slug)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// Ref is a lightweight summary of an on-disk customization, returned
// by listing operations so the UI can render "you have N customized
// rules across these subscriptions" without parsing every diff payload.
type Ref struct {
	Subscription string    `json:"subscription"`
	Slug         string    `json:"slug"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// List walks <stateDir>/customizations and returns one Ref per
// stored customization across all subscriptions. Missing root dir
// returns an empty slice without error — "no customizations yet" is
// the default state.
//
// Sorted by (subscription, slug) for deterministic output (matters
// for UI list rendering + golden-file tests).
func List(stateDir string) ([]Ref, error) {
	root := customizationsDir(stateDir)
	subs, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var refs []Ref
	for _, sub := range subs {
		if !sub.IsDir() {
			continue
		}
		subRefs, err := ListForSubscription(stateDir, sub.Name())
		if err != nil {
			return nil, err
		}
		refs = append(refs, subRefs...)
	}
	return refs, nil
}

// ListForSubscription returns refs scoped to a single subscription.
// Used by the per-subscription detail view + by apply-on-pull to know
// which rules need diff re-application.
func ListForSubscription(stateDir, sub string) ([]Ref, error) {
	if sub == "" {
		return nil, fmt.Errorf("sub is required")
	}
	dir := filepath.Join(customizationsDir(stateDir), sub)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var refs []Ref
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		// Lazy load — UpdatedAt comes from the file itself, not the
		// inode mtime (which would be touched by backup tools / rsync /
		// container restarts). Cost: one extra read per ref, but lists
		// are small (≤ a few hundred customizations per subscription).
		slug := strings.TrimSuffix(e.Name(), ".json")
		c, err := Load(stateDir, sub, slug)
		if err != nil || c == nil {
			continue
		}
		refs = append(refs, Ref{
			Subscription: sub,
			Slug:         slug,
			UpdatedAt:    c.UpdatedAt,
		})
	}
	return refs, nil
}
