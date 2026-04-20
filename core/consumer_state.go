package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConsumerState lives at <repo_dir>/.qui-sync/consumer.state.json (so it
// sits next to maintainer.state.json and shares gitignored/backup paths).
// Holds everything the Sync tab needs to know across restarts: which
// repos are subscribed, which local rule maps to which Qui rule, and
// per-rule user overrides.
type ConsumerState struct {
	mu            sync.RWMutex                      `json:"-"`
	Version       int                               `json:"version"`
	Subscriptions map[string]*SubscriptionStateData `json:"subscriptions"`
}

// SubscriptionStateData is one tracked git repo.
// Skip status lives per-rule in Rules[path].LinkedAction == "skip" —
// no separate Skipped list to keep in sync.
type SubscriptionStateData struct {
	URL         string                       `json:"url"`
	Branch      string                       `json:"branch"`
	Auth        SubscriptionAuth             `json:"auth"`
	AddedAt     time.Time                    `json:"added_at"`
	LastPullSHA string                       `json:"last_pull_sha,omitempty"`
	LastPullAt  time.Time                    `json:"last_pull_at,omitempty"`
	Rules       map[string]*SubRuleStateData `json:"rules,omitempty"`
	// TargetInstanceID is the Qui instance auto-sync applies to.
	// Set when user first runs Plan and picks an instance; persisted
	// so auto-sync knows where to write without user interaction.
	TargetInstanceID int `json:"target_instance_id,omitempty"`
	// TargetCategory filters which repo rules auto-sync considers.
	// Empty string = all categories.
	TargetCategory string `json:"target_category,omitempty"`
}

// SubscriptionAuth mirrors GitAuth but is the persisted, on-disk form.
// Token is never stored here — only the path to the key/token file.
type SubscriptionAuth struct {
	Mode      GitAuthMode `json:"mode"`
	KeyFile   string      `json:"key_file,omitempty"`   // ssh_deploy_key
	TokenFile string      `json:"token_file,omitempty"` // token mode — read at apply time
}

// SubRuleStateData is one rule's mapping, remembered across applies so
// the user's linking decisions persist.
type SubRuleStateData struct {
	// QuiInstanceID the rule is applied to. 0 until first plan+apply.
	QuiInstanceID int `json:"qui_instance_id,omitempty"`
	// QuiRuleID is Qui's server-assigned id. 0 for create_new before first apply.
	QuiRuleID int `json:"qui_rule_id,omitempty"`
	// LinkedAction: "create_new", "update_existing", or "skip".
	LinkedAction string `json:"linked_action,omitempty"`
	// AppliedSHA tracks the upstream file SHA at last successful apply.
	// v0.3 will use it for drift detection; v0.2 just records it.
	AppliedSHA string    `json:"applied_sha,omitempty"`
	AppliedAt  time.Time `json:"applied_at,omitempty"`
	// SortOrderOverride: user-picked priority. Nil means "use repo's value".
	SortOrderOverride *int `json:"sort_order_override,omitempty"`
	// PreservedFields: which Qui-rule fields never get overwritten on update.
	// Defaults to DefaultPreservedFields() when the entry is first written.
	PreservedFields []string `json:"preserved_fields,omitempty"`
	// AutoSync: when true, the background worker applies this rule
	// automatically on each pull that detects changes. When false
	// (default), the rule requires manual Plan + Apply.
	AutoSync bool `json:"auto_sync,omitempty"`
}

// DefaultPreservedFields lists the user-owned rule fields that survive
// an update-existing apply. If a user needs a different set per rule,
// they'll be able to edit it in v0.3; for MVP this list is the default
// baked into every new rule entry.
func DefaultPreservedFields() []string {
	return []string{
		"trackerPattern",
		"trackerDomains",
		"intervalSeconds",
		"freeSpaceSource",
		"enabled",
		"dryRun",
		"notify",
	}
}

func consumerStatePath(stateDir string) string {
	return filepath.Join(stateDir, "consumer.state.json")
}

// LoadConsumerState reads consumer.state.json. Returns an empty state
// if the file doesn't exist yet (fresh install).
func LoadConsumerState(repoDir string) (*ConsumerState, error) {
	path := consumerStatePath(repoDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &ConsumerState{Version: 1, Subscriptions: map[string]*SubscriptionStateData{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read consumer state: %w", err)
	}
	var s ConsumerState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse consumer state: %w", err)
	}
	if s.Subscriptions == nil {
		s.Subscriptions = map[string]*SubscriptionStateData{}
	}
	if s.Version == 0 {
		s.Version = 1
	}
	return &s, nil
}

// Save writes state atomically (tmp + rename).
func (s *ConsumerState) Save(repoDir string) error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	path := consumerStatePath(repoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Subscription returns the data for a given slug, creating an empty entry
// on first access. Caller must hold the write lock if using the result
// for mutation; for reads, prefer SubscriptionSnapshot.
func (s *ConsumerState) Subscription(slug string) *SubscriptionStateData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscriptionLocked(slug)
}

func (s *ConsumerState) subscriptionLocked(slug string) *SubscriptionStateData {
	sub := s.Subscriptions[slug]
	if sub == nil {
		sub = &SubscriptionStateData{
			AddedAt: time.Now().UTC(),
			Rules:   map[string]*SubRuleStateData{},
		}
		s.Subscriptions[slug] = sub
	}
	if sub.Rules == nil {
		sub.Rules = map[string]*SubRuleStateData{}
	}
	return sub
}

// SubscriptionSnapshot returns a deep-enough copy of one subscription's
// state so callers can read without holding the lock during iteration.
func (s *ConsumerState) SubscriptionSnapshot(slug string) *SubscriptionStateData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.Subscriptions[slug]
	if src == nil {
		return nil
	}
	cp := *src
	cp.Rules = make(map[string]*SubRuleStateData, len(src.Rules))
	for k, v := range src.Rules {
		if v == nil {
			continue
		}
		rc := *v
		cp.Rules[k] = &rc
	}
	return &cp
}

// ListSubscriptionSlugs returns configured slugs in deterministic order.
func (s *ConsumerState) ListSubscriptionSlugs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.Subscriptions))
	for k := range s.Subscriptions {
		out = append(out, k)
	}
	// Caller can sort if needed — json map iteration is random, but
	// the Save path uses json.MarshalIndent which sorts by key.
	return out
}

// AddSubscription registers a new subscription. Returns an error if slug
// already exists. The rest of the fields (LastPull*, Rules) start empty.
// targetCategory is optional — empty means "all categories".
func (s *ConsumerState) AddSubscription(slug, url, branch string, auth SubscriptionAuth, targetCategory string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.Subscriptions[slug]; exists {
		return fmt.Errorf("subscription %q already exists", slug)
	}
	s.Subscriptions[slug] = &SubscriptionStateData{
		URL:            url,
		Branch:         branch,
		Auth:           auth,
		AddedAt:        time.Now().UTC(),
		TargetCategory: targetCategory,
		Rules:          map[string]*SubRuleStateData{},
	}
	return nil
}

// UpdateSubscription updates the editable fields of an existing
// subscription in place. Slug is immutable (it's the clone-dir name).
// URL change is allowed but the caller is responsible for invalidating
// the on-disk clone if the new URL points elsewhere.
func (s *ConsumerState) UpdateSubscription(slug, url, branch string, auth SubscriptionAuth, targetCategory string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, exists := s.Subscriptions[slug]
	if !exists {
		return fmt.Errorf("subscription %q not found", slug)
	}
	sub.URL = url
	sub.Branch = branch
	sub.Auth = auth
	sub.TargetCategory = targetCategory
	return nil
}

// RemoveSubscription deletes a subscription from state. Callers are
// responsible for cleaning up the clone directory + auth key files.
func (s *ConsumerState) RemoveSubscription(slug string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.Subscriptions[slug]; !exists {
		return false
	}
	delete(s.Subscriptions, slug)
	return true
}

// SetRuleDecision records the user's per-rule plan decision. Creates the
// rule entry if missing. action must be one of: create_new, update_existing,
// skip. qui_rule_id is required for update_existing, ignored otherwise.
func (s *ConsumerState) SetRuleDecision(slug, repoPath string, instanceID, quiRuleID int, action string, sortOverride *int) error {
	switch action {
	case "create_new", "update_existing", "skip":
	default:
		return fmt.Errorf("invalid action %q", action)
	}
	if action == "update_existing" && quiRuleID == 0 {
		return fmt.Errorf("update_existing requires qui_rule_id")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subscriptionLocked(slug)
	r := sub.Rules[repoPath]
	if r == nil {
		r = &SubRuleStateData{PreservedFields: DefaultPreservedFields()}
		sub.Rules[repoPath] = r
	}
	r.QuiInstanceID = instanceID
	r.QuiRuleID = quiRuleID
	r.LinkedAction = action
	r.SortOrderOverride = sortOverride
	return nil
}

// RecordApply stamps the applied_sha and time on a rule after a successful
// create or update. Called by the sync engine; not exposed via API.
func (s *ConsumerState) RecordApply(slug, repoPath string, quiRuleID int, appliedSHA string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subscriptionLocked(slug)
	r := sub.Rules[repoPath]
	if r == nil {
		r = &SubRuleStateData{PreservedFields: DefaultPreservedFields()}
		sub.Rules[repoPath] = r
	}
	if quiRuleID > 0 {
		r.QuiRuleID = quiRuleID
	}
	r.AppliedSHA = appliedSHA
	r.AppliedAt = time.Now().UTC()
}

// RecordPull stamps last-pull metadata after a successful fetch.
func (s *ConsumerState) RecordPull(slug, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subscriptionLocked(slug)
	sub.LastPullSHA = sha
	sub.LastPullAt = time.Now().UTC()
}
