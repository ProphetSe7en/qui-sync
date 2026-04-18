package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	Backup          BackupConfig     `yaml:"backup,omitempty"`

	// Sync settings
	AutoPullInterval string `yaml:"auto_pull_interval,omitempty" json:"auto_pull_interval,omitempty"` // off, 5m, 1h, 6h, 12h, 24h

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

type BackupConfig struct {
	RetentionDays int  `yaml:"retention_days"`
	Gitignored    bool `yaml:"gitignored"`
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
		cfg.RepoDir = "/data"
	}

	return &cfg, nil
}

// defaultConfig returns a working-but-empty config for first-run.
// The user completes setup via Settings in the UI.
func defaultConfig(path string) *Config {
	return &Config{
		Mode:    "maintainer",
		RepoDir: "/data",
		Backup:  BackupConfig{RetentionDays: 90, Gitignored: true},
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
		"trackerPattern",
		"trackerDomains",
		"intervalSeconds",
		"freeSpaceSource",
		"enabled",
		"dryRun",
		"notify",
		// Note: sortOrder is intentionally preserved. It flows from Qui
		// through qui-sync, can be overridden in the UI per rule, and
		// ends up in the exported file so consumers get the maintainer's
		// intended priority.
	}
}
