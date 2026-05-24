package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// Config is the root configuration. Mode decides which subset is required.
type Config struct {
	Mode string    `yaml:"mode"` // "maintainer" or "consumer"
	Qui  QuiConfig `yaml:"qui"`

	// Maintainer mode
	RepoDir         string           `yaml:"repo_dir,omitempty"`
	ExportInstances []ExportInstance `yaml:"export_instances,omitempty"`
	StripFields     []string         `yaml:"strip_fields,omitempty"`
	ReverseMappings Mappings         `yaml:"reverse_mappings,omitempty"`

	// BackupSchedules is the active list of named backup rules, each
	// with its own cron, instance set, and retention. v0.3 replaces
	// the old single-schedule `backup:` block with this list — the
	// migration in LoadConfig converts the old shape into the first
	// entry (named "Default backup") on first read.
	BackupSchedules []BackupSchedule `yaml:"backup_schedules,omitempty"`

	// BackupGitignored is the global "hide backups from GitHub" toggle.
	// Applies to every schedule — there's no per-schedule reason to
	// publish backups since the backups-full directory is shared by all.
	BackupGitignored bool `yaml:"backup_gitignored,omitempty"`

	// ArchiveGitignored is the "hide archived rules from GitHub" toggle.
	// archive/ lives INSIDE the share-repo (always, by definition), so
	// when this is on we write an "archive/" entry to .gitignore. Users
	// who treat the archive as a private soft-delete record (and don't
	// want consumers to see the history) flip this on; users who want
	// the archive published as part of the canonical share-repo leave
	// it off (the default). Toggle applies on the next Commit export or
	// Push to remote — the gitignore write itself is immediate but only
	// takes effect when staged in the next commit.
	ArchiveGitignored bool `yaml:"archive_gitignored,omitempty"`

	// RepoDisplayName is the friendly label shown in the "Where to
	// publish" card on the Publish tab. Optional — when blank the UI
	// derives a label from the URL (last path segment minus .git).
	// Stored here rather than in .git/config so it survives a remote
	// URL change and a `git remote remove origin`.
	RepoDisplayName string `yaml:"repo_display_name,omitempty"`

	// Backup is the legacy single-schedule shape. Read once on Load for
	// migration; never written back. New code reads BackupSchedules.
	Backup BackupConfig `yaml:"backup,omitempty"`

	// Sync settings
	AutoPullInterval string `yaml:"auto_pull_interval,omitempty" json:"auto_pull_interval,omitempty"` // off, 5m, 1h, 6h, 12h, 24h

	// Authentication — Radarr/Sonarr-parity model. See internal/auth.
	// Defaults: forms + disabled_for_local_addresses + 30-day session.
	// Empty values mean "use the auth package default" so an unmigrated
	// config doesn't lock the user out — first-run /setup wizard takes
	// over until credentials exist.
	Authentication         string `yaml:"authentication,omitempty"`           // forms | basic | none
	AuthenticationRequired string `yaml:"authentication_required,omitempty"` // enabled | disabled_for_local_addresses
	SessionTTLDays         int    `yaml:"session_ttl_days,omitempty"`         // 0 → 30 days
	TrustedNetworks        string `yaml:"trusted_networks,omitempty"`         // comma-separated CIDRs/IPs
	TrustedProxies         string `yaml:"trusted_proxies,omitempty"`          // comma-separated IPs

	// Consumer mode (reserved for v0.2+)
	Source           *SourceConfig             `yaml:"source,omitempty"`
	ApplyInstances   []ApplyInstance           `yaml:"apply_instances,omitempty"`
	Mappings         Mappings                  `yaml:"mappings,omitempty"`
	NewRuleDefaults  map[string]any            `yaml:"new_rule_defaults,omitempty"`
	PerRuleOverrides map[string]map[string]any `yaml:"per_rule_overrides,omitempty"`
	OrphanBehavior   string                    `yaml:"orphan_behavior,omitempty"`
	Notifications    *NotificationsConfig      `yaml:"notifications,omitempty"`
}

type QuiConfig struct {
	URL        string `yaml:"url"`
	APIKey     string `yaml:"api_key,omitempty"`
	APIKeyFile string `yaml:"api_key_file,omitempty"`
	// BrowserURL is the URL the USER'S BROWSER uses to reach Qui's
	// web UI. Distinct from URL, which is the server-to-server URL
	// qui-sync uses to talk to Qui's API and may be a Docker container
	// name (`http://qui:7476`), a localhost path, or otherwise not
	// reachable from a remote browser. When BrowserURL is empty the
	// UI falls back to URL — fine for users running everything on
	// one machine, breaks for reverse-proxy + Docker-network setups.
	// Surfaced in the "Open in Qui" deep-link from the Customize
	// flow; empty value disables the button (template short-circuits
	// to null).
	BrowserURL string `yaml:"browser_url,omitempty"`
}

type ExportInstance struct {
	QuiInstanceID int `yaml:"qui_instance_id" json:"qui_instance_id"`
	// Category is the folder name under rules/ on disk, e.g. "movies".
	// What ends up in the share-repo. User controls this by editing config.yml.
	Category string `yaml:"category" json:"category"`
}

type ApplyInstance struct {
	QuiInstanceID int      `yaml:"qui_instance_id"`
	Categories    []string `yaml:"categories"`
}

type Mappings struct {
	Tags       map[string]string `yaml:"tags,omitempty"`
	Categories map[string]string `yaml:"categories,omitempty"`
	Paths      map[string]string `yaml:"paths,omitempty"`
}

// BackupSchedule is a named, cron-scheduled rule that snapshots the
// listed Qui instances on its cadence. Multiple schedules can exist
// independently — e.g. "TV nightly" on instance 1 every day at 03:00,
// "Weekly full" on every instance every Sunday. Schedules with empty
// Instances back up every configured ExportInstance (the "everything"
// fallback for users who don't want to think about scoping).
type BackupSchedule struct {
	// ID is a stable slug (e.g. "tv-nightly"), generated server-side
	// when the schedule is created. Used by the API for update/delete
	// and by the UI as the React-key equivalent. Never user-edited.
	ID string `yaml:"id" json:"id"`

	// Name is the user-facing label shown on the schedule card.
	// Defaults to "Backup" when blank.
	Name string `yaml:"name" json:"name"`

	// Cron is the 5-field cron expression. Empty string or invalid
	// parse → schedule treated as inactive regardless of Enabled.
	Cron string `yaml:"cron" json:"cron"`

	// Enabled is an independent on/off toggle so a schedule can be
	// paused without losing its cron — flipping it back resumes the
	// previous cadence. Same pattern as the v0.2 single-schedule.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Instances is the set of qui_instance_ids this schedule snapshots.
	// Empty means "every configured ExportInstance" — preserves the
	// pre-v0.3 single-schedule behaviour for migrated configs.
	Instances []int `yaml:"instances,omitempty" json:"instances,omitempty"`

	// RetentionDays is the age threshold for automatic pruning of this
	// schedule's snapshots. Snapshots older than RetentionDays are
	// candidates for deletion, but only beyond the KeepLastN floor —
	// the two rules combine so we never drop below the recent-N safety
	// net even when every snapshot is past retention.
	RetentionDays int `yaml:"retention_days" json:"retention_days"`

	// KeepLastN is the minimum number of snapshots kept per instance
	// regardless of age. 0 means "rely on RetentionDays alone".
	KeepLastN int `yaml:"keep_last_n" json:"keep_last_n"`
}

// BackupConfig is the legacy single-schedule shape. Read once at
// LoadConfig for migration into BackupSchedules; not written back.
type BackupConfig struct {
	Cron          string `yaml:"cron"`
	Enabled       bool   `yaml:"enabled"`
	RetentionDays int    `yaml:"retention_days"`
	KeepLastN     int    `yaml:"keep_last_n"`
	Gitignored    bool   `yaml:"gitignored"`
}

type SourceConfig struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

type NotificationsConfig struct {
	DiscordWebhook string `yaml:"discord_webhook,omitempty"`
	OnApply        string `yaml:"on_apply,omitempty"` // summary | verbose | off
	OnNoChanges    bool   `yaml:"on_no_changes,omitempty"`
}

// LoadConfig reads a YAML config file. If the file doesn't exist, a minimal
// default is created automatically so the server can start and the user can
// configure everything via the UI — no manual file editing required.
func LoadConfig(path string) (*Config, error) {
	path = ExpandHome(path)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg := defaultConfig(path)
		if err := SaveConfig(path, cfg); err != nil {
			return nil, fmt.Errorf("create default config: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.Qui.APIKeyFile = ExpandHome(cfg.Qui.APIKeyFile)
	cfg.RepoDir = ExpandHome(cfg.RepoDir)

	// Fill defaults for fields that must have a value even if config is sparse.
	if cfg.Mode == "" {
		cfg.Mode = "maintainer"
	}
	if cfg.RepoDir == "" {
		cfg.RepoDir = "/data/repo"
	}

	// Migrate legacy `backup:` block (pre-v0.3 single schedule) into the
	// first BackupSchedules entry. Idempotent — runs only when the new
	// list is empty AND the old block has any data. Persists immediately
	// so subsequent loads find the canonical shape and skip migration.
	if migrated := migrateLegacyBackup(&cfg); migrated {
		if err := SaveConfig(path, &cfg); err != nil {
			return nil, fmt.Errorf("persist migrated backup schedule: %w", err)
		}
	}

	return &cfg, nil
}

// migrateLegacyBackup converts the pre-v0.3 single-schedule `backup:`
// block into the new BackupSchedules list. Returns true when a write-
// back is needed. Empty Instances on the migrated entry means "every
// configured ExportInstance", which preserves the pre-v0.3 fan-out.
func migrateLegacyBackup(cfg *Config) bool {
	if len(cfg.BackupSchedules) > 0 {
		// Already on the new shape — clear any stray legacy fields so
		// the next save writes a clean YAML without the old block.
		if cfg.Backup != (BackupConfig{}) {
			cfg.Backup = BackupConfig{}
			return true
		}
		return false
	}
	legacy := cfg.Backup
	if legacy.Cron == "" && !legacy.Enabled && legacy.RetentionDays == 0 && legacy.KeepLastN == 0 && !legacy.Gitignored {
		// Fresh install with no legacy data — nothing to migrate.
		return false
	}
	cfg.BackupSchedules = []BackupSchedule{
		{
			ID:            "default",
			Name:          "Default backup",
			Cron:          legacy.Cron,
			Enabled:       legacy.Enabled,
			Instances:     nil, // empty → all configured ExportInstances
			RetentionDays: legacy.RetentionDays,
			KeepLastN:     legacy.KeepLastN,
		},
	}
	cfg.BackupGitignored = legacy.Gitignored
	cfg.Backup = BackupConfig{} // clear legacy block; SaveConfig writes new shape
	return true
}

// defaultConfig returns a working-but-empty config for first-run.
// The user completes setup via Settings in the UI.
func defaultConfig(path string) *Config {
	return &Config{
		Mode:             "maintainer",
		RepoDir:          "/data/repo",
		BackupGitignored: true,
		// Empty BackupSchedules — user creates their first schedule on
		// the Backup tab. No silent default schedule firing without the
		// user knowing it exists.
	}
}

// DataPaths computes the canonical subdirectory layout under the data volume.
// RepoDir is the git share-repo (push this). Everything else is local-only.
type DataPaths struct {
	Repo        string // /data/repo — the git share-repo
	State       string // /data/state — maintainer + consumer state
	Backups     string // /data/backups — archived rule versions (per-rule on export)
	FullBackups string // /data/backups-full — full instance backups (Backup tab)
	Sources     string // /data/sources — sync subscription clones
}

// Paths derives the canonical subdirectories from RepoDir.
// If RepoDir is /data/repo, sibling dirs are /data/state, etc.
// If RepoDir is a volume root like /data (compact config, no /repo
// subdir), subdirs go INSIDE RepoDir instead of alongside — prevents
// writing to filesystem root "/" which is read-only for nobody:users.
func (c *Config) Paths() DataPaths {
	parent := filepath.Dir(c.RepoDir)
	if parent == "/" || parent == "." {
		parent = c.RepoDir
	}
	return DataPaths{
		Repo:        c.RepoDir,
		State:       filepath.Join(parent, "state"),
		Backups:     filepath.Join(parent, "backups"),
		FullBackups: filepath.Join(parent, "backups-full"),
		Sources:     filepath.Join(parent, "sources"),
	}
}

// IsQuiConfigured returns true when the user has set a Qui URL + API key.
// Used by handlers to gate operations that need Qui access — the server
// itself starts without Qui configured (first-run setup via UI).
func (c *Config) IsQuiConfigured() bool {
	return c.Qui.URL != "" && (c.Qui.APIKey != "" || c.Qui.APIKeyFile != "")
}

// ResolveAPIKey returns the API key, reading from file if APIKeyFile is set.
func (c *Config) ResolveAPIKey() (string, error) {
	if c.Qui.APIKey != "" {
		return c.Qui.APIKey, nil
	}
	data, err := os.ReadFile(c.Qui.APIKeyFile)
	if err != nil {
		return "", fmt.Errorf("read api_key_file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// SaveConfig writes the config back to disk. Comments are not preserved
// (yaml.v3 roundtrip loses them). A managed-by header is prepended so users
// realise the file is being edited programmatically.
func SaveConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	header := "# Managed by qui-sync — edits from the UI overwrite comments.\n# Hand-edits are still respected but formatting may change next UI save.\n\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(header+string(data)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ParsePullInterval converts the config string to a time.Duration.
// Returns 0 for "off" or empty (= disabled).
func ParsePullInterval(s string) time.Duration {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "5m":
		return 5 * time.Minute
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "24h":
		return 24 * time.Hour
	}
	return 0 // off or unrecognised
}

// ParseBackupCron validates a cron expression using the same grammar
// the BackupScheduler uses at runtime (robfig/cron/v3 standard parser —
// 5-field Unix cron plus @daily/@weekly/@monthly shortcuts). Returns
// the compiled schedule when valid, or an error when empty / malformed.
// Keeping parsing in one place so UI validation + scheduler dispatch
// can't drift apart.
func ParseBackupCron(expr string) (cron.Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("cron expression is empty")
	}
	return cron.ParseStandard(expr)
}

// DefaultConfigPath returns ~/.config/qui-sync/config.yml
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yml"
	}
	return filepath.Join(home, ".config", "qui-sync", "config.yml")
}

func ExpandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// DefaultStripFields returns the fields we strip from upstream rule files.
// Per SPEC, user-owned fields (consumer's choice) are never shipped via upstream.
func DefaultStripFields() []string {
	return []string{
		// Identity/timestamps — never useful upstream.
		"id",
		"instanceId",
		"createdAt",
		"updatedAt",
		// Consumer-owned (user's local values — upstream must not dictate).
		// trackerPattern and trackerDomains are NOT stripped — they're replaced
		// with placeholders ("tracker_1" / ["tracker.xyz"]) in buildRuleFile
		// so consumers know the fields exist. Wildcard "*" is preserved as-is.
		"freeSpaceSource",
		"enabled",
		"dryRun",
		"notify",
		// Note: intervalSeconds is intentionally preserved — it's part of
		// the rule logic (how often Qui checks the rule). Consumers should
		// get the maintainer's recommended interval.
		// Note: sortOrder is intentionally preserved. It flows from Qui
		// through qui-sync, can be overridden in the UI per rule, and
		// ends up in the exported file so consumers get the maintainer's
		// intended priority.
	}
}
