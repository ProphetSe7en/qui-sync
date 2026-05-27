// storage_v2.go — on-disk persistence for the v0.6+ customize format.
//
// Reuses the safety primitives from storage.go (validIdentifier,
// filePath layout). The file format is the same JSON envelope on disk
// — only the schemaVersion + payload shape differ.
//
//   v0.5 file (handled by storage.go):
//     {"version": 1, "schema_version_assumed": "1",
//      "diff": <RFC 6902 JSON Patch array>, ...}
//
//   v2 file (handled by THIS file):
//     {"schemaVersion": "2", "ruleSchemaVersion": "1",
//      "ops": [<Op>, ...], ...}
//
// Phase F's MaybeMigrate moves any v0.5 file out of the customizations
// tree on first boot; after that, only v2 files live there.

package customize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SaveV2 writes the customization for (sub, slug). Returns ErrInvalidIdentifier
// when either segment fails validation — defends against maintainer-supplied
// rule slugs containing path separators.
//
// Atomicity: writes to a sibling .tmp file, then renames into place.
// If the write fails mid-flight, the existing file (if any) is untouched.
func SaveV2(stateDir, sub, slug string, c *CustomizationV2) error {
	if sub == "" || slug == "" {
		return fmt.Errorf("sub and slug are required")
	}
	if !validIdentifier(sub) || !validIdentifier(slug) {
		return ErrInvalidIdentifier
	}
	if c == nil {
		return fmt.Errorf("customization is nil")
	}

	// B4 fix: ALWAYS stamp the canonical schema version. A caller
	// passing a wrong/stale value would otherwise persist a file
	// LoadV2 then rejects — silent data-loss until someone opens
	// the file. Forced-overwrite makes "what's on disk" predictable.
	c.SchemaVersion = CurrentSchemaVersion
	if c.CapturedAt == "" {
		c.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// B5 fix: serialise concurrent writes for the same (sub, slug).
	// Without this, handlers doing Load → mutate → Save could race
	// and lose updates. Same lock the v0.5 Save uses (locks.go).
	mu := SaveLockFor(sub, slug)
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Join(customizationsDir(stateDir), sub), 0o755); err != nil {
		return fmt.Errorf("mkdir customizations subdir: %w", err)
	}
	path := filePath(stateDir, sub, slug)

	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort cleanup; the tmp file is harmless if it lingers.
		_ = os.Remove(tmp)
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

// LoadV2 reads the customization for (sub, slug) and decodes it as a
// CustomizationV2. Returns nil + nil for "no file" (the normal
// not-customized state).
//
// If the file is in v0.5 format (no `schemaVersion` field or value
// other than "2"), returns ErrUnsupportedVersion so callers can route
// to the migration path or surface a "needs recreate" banner.
func LoadV2(stateDir, sub, slug string) (*CustomizationV2, error) {
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
		return nil, fmt.Errorf("read: %w", err)
	}

	// Inspect the schemaVersion field cheaply before full decode — this
	// lets us produce a typed error for v0.5 files without first
	// half-decoding them into the wrong struct.
	var envelope struct {
		SchemaVersion any `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	sv, _ := envelope.SchemaVersion.(string)
	if sv != CurrentSchemaVersion {
		return nil, fmt.Errorf("%w: file at %s has schemaVersion=%q, want %q",
			ErrUnsupportedV2Version, path, sv, CurrentSchemaVersion)
	}

	var c CustomizationV2
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decode customization: %w", err)
	}
	return &c, nil
}

// DeleteV2 removes the customization file for (sub, slug). Returns
// nil if the file doesn't exist — idempotent.
func DeleteV2(stateDir, sub, slug string) error {
	if sub == "" || slug == "" {
		return fmt.Errorf("sub and slug are required")
	}
	if !validIdentifier(sub) || !validIdentifier(slug) {
		return ErrInvalidIdentifier
	}
	// Hold the same lock as SaveV2 so an in-flight save can't be
	// half-renamed when delete races. Trivial cost, prevents
	// surprising disk states.
	mu := SaveLockFor(sub, slug)
	mu.Lock()
	defer mu.Unlock()
	path := filePath(stateDir, sub, slug)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ListV2ForSubscription returns the slug list of all v2 customizations
// in the given subscription. Files that are NOT v2 (v0.5 holdovers
// that Phase F migration hasn't archived yet, or future formats) are
// silently skipped — the caller is interested in "what's customized
// today", not "what files exist".
//
// Returns nil + nil when the subscription dir doesn't exist (no
// customizations yet, normal state).
func ListV2ForSubscription(stateDir, sub string) ([]string, error) {
	if sub == "" {
		return nil, fmt.Errorf("sub is required")
	}
	if !validIdentifier(sub) {
		return nil, ErrInvalidIdentifier
	}
	dir := filepath.Join(customizationsDir(stateDir), sub)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		slug := strings.TrimSuffix(name, ".json")
		// Peek the file's schemaVersion. Skip non-v2 silently.
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var envelope struct {
			SchemaVersion any `json:"schemaVersion"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			continue
		}
		if sv, _ := envelope.SchemaVersion.(string); sv == CurrentSchemaVersion {
			out = append(out, slug)
		}
	}
	return out, nil
}

// ErrUnsupportedV2Version signals that LoadV2 was given a file in a
// format other than schemaVersion="2" (typically a v0.5 holdover that
// Phase F hasn't archived yet, OR a forward-incompatible future
// format). Callers can either: (a) skip the file (current sync flow,
// "no customize" semantics), (b) route to migration UI, or (c) fail
// hard if the file is required.
//
// Wrap with errors.Is for caller branching.
var ErrUnsupportedV2Version = fmt.Errorf("customize: file is not v2 format")
