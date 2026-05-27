// migrate.go — one-time v0.5 → v0.6 customize-format migration.
//
// Strategy (per design §6, Q3 decision): archive v0.5 files and ask the
// user to recreate. No automated conversion — v0.5 patches are themselves
// buggy (positional ops destabilised by maintainer reorgs); auto-converting
// would faithfully preserve the bug in v0.6 format.
//
// Phase A: types + signatures only. Implementation lands in Phase F.

package customize

// MigrationResult is the outcome of one MaybeMigrate call, surfaced to
// the UI via the migration-status endpoint and used to render the
// one-time banner.
type MigrationResult struct {
	// Performed is true when this boot actually moved files. False
	// means either the migration flag was already set OR no v0.5
	// files were found.
	Performed bool `json:"performed"`

	// ArchivedFiles lists the relative paths (relative to the
	// customize root) of every file that was moved into the archive.
	// Empty when Performed=false. UI uses len() to render the count
	// in the banner.
	ArchivedFiles []string `json:"archived_files"`

	// ArchiveRoot is the absolute path where archived files now live.
	// Surfaced in the banner so testers can grep through them if
	// curious. v0.7 deletes the directory entirely.
	ArchiveRoot string `json:"archive_root"`

	// MigratedAt is the ISO 8601 UTC timestamp when migration ran.
	// Empty when Performed=false. Stored in the once-only flag file
	// so subsequent boots can show "migrated 2 days ago" if needed.
	MigratedAt string `json:"migrated_at,omitempty"`
}

// MaybeMigrate runs the one-time scan-and-archive flow.
//
//   customizeRoot: directory containing per-(sub, slug) customize JSON files
//                  (typically <state>/customizations/)
//   archiveRoot:   parent directory for archive subtree (typically <state>/customizations-archive/)
//                  Files land under archiveRoot/v0.5/<sub>/<slug>.json
//   stateDir:      directory for the once-only flag file
//                  (typically the same directory holding state.json)
//
// Idempotent: if the flag file at stateDir/.customize_v06_migrated exists,
// returns Performed=false without scanning. Safe to call on every boot.
//
// Returns error only on filesystem I/O failures. A missing customizeRoot
// (fresh install with no customizations) is NOT an error — returns
// Performed=false silently.
//
// Caller (cmd/server/main.go) logs failures but does not fatal — a
// migration error means the user might see v0.5 files re-attempted at
// runtime, where they'll fail to load and be treated as not-found
// (graceful degradation).
func MaybeMigrate(customizeRoot, archiveRoot, stateDir string) (*MigrationResult, error) {
	// TODO Phase F — small (<60 line) implementation.
	return &MigrationResult{Performed: false}, nil
}

// migrationFlagFile returns the path to the once-only flag.
// Centralised so cmd/server/main.go and tests can reference the same path.
func migrationFlagFile(stateDir string) string {
	// TODO Phase F.
	return ""
}
