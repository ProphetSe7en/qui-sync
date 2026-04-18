package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// MaintainerState maps Qui instance IDs and rule IDs to stable slugs.
// Lives at <repo_dir>/.qui-sync/maintainer.state.json.
//
// All public methods are safe for concurrent use. The CLI is single-threaded
// so the mutex is a no-op cost, but the HTTP server has overlapping requests
// (e.g. GET /api/rules during POST /api/export/run).
type MaintainerState struct {
	mu        sync.RWMutex                  `json:"-"`
	Version   int                           `json:"version"`
	Instances map[string]*InstanceStateData `json:"instances"`
	// ExcludedRules[instanceID][ruleID] = true marks rules the user has
	// opted out of exporting. Excluded rules are skipped entirely — not
	// shown as Added/Updated/Removed, not written to disk, not logged
	// in the changelog. Toggling exclude off resumes normal processing.
	ExcludedRules map[string]map[string]bool `json:"excluded_rules,omitempty"`
	// ExcludedInstances[instanceID] = true skips an entire instance from
	// export, including any new rules that appear in Qui afterwards.
	// Takes precedence over per-rule excludes.
	ExcludedInstances map[string]bool `json:"excluded_instances,omitempty"`
	// SortOrderOverrides[instanceID][ruleID] = intended sort order for
	// the exported file. Used to override Qui's sortOrder at export
	// without touching Qui itself. Maintainer's Qui is never modified.
	SortOrderOverrides map[string]map[string]int `json:"sort_order_overrides,omitempty"`
}

type InstanceStateData struct {
	// Rules keyed by Qui rule ID (as string) → slug + last-known name.
	Rules map[string]*RuleStateEntry `json:"rules"`
}

type RuleStateEntry struct {
	Slug     string `json:"slug"`
	LastName string `json:"last_name"`
	Category string `json:"category"`
}

// LoadMaintainerState reads state from repoDir. Returns an empty state if missing.
func LoadMaintainerState(repoDir string) (*MaintainerState, error) {
	path := maintainerStatePath(repoDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &MaintainerState{Version: 1, Instances: map[string]*InstanceStateData{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s MaintainerState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.Instances == nil {
		s.Instances = map[string]*InstanceStateData{}
	}
	if s.Version == 0 {
		s.Version = 1
	}
	return &s, nil
}

// Save writes state atomically (write to tmp, rename).
func (s *MaintainerState) Save(repoDir string) error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	path := maintainerStatePath(repoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Instance returns (or creates) the state for a given Qui instance.
// Only use this when you're about to write to the instance. For reads,
// prefer instanceOrEmpty. Caller must hold the write lock.
func (s *MaintainerState) instanceLocked(quiInstanceID int) *InstanceStateData {
	key := strconv.Itoa(quiInstanceID)
	if s.Instances[key] == nil {
		s.Instances[key] = &InstanceStateData{Rules: map[string]*RuleStateEntry{}}
	}
	if s.Instances[key].Rules == nil {
		s.Instances[key].Rules = map[string]*RuleStateEntry{}
	}
	return s.Instances[key]
}

// Instance is the public, lock-acquiring variant for callers that want to
// iterate a specific instance's rules. The returned slice is a snapshot.
func (s *MaintainerState) Instance(quiInstanceID int) *InstanceStateData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instanceLocked(quiInstanceID)
}

// instanceOrEmptyLocked returns the instance state read-only — never creates.
// Caller must hold the read lock.
func (s *MaintainerState) instanceOrEmptyLocked(quiInstanceID int) *InstanceStateData {
	key := strconv.Itoa(quiInstanceID)
	if s.Instances[key] == nil {
		return &InstanceStateData{Rules: map[string]*RuleStateEntry{}}
	}
	if s.Instances[key].Rules == nil {
		return &InstanceStateData{Rules: map[string]*RuleStateEntry{}}
	}
	return s.Instances[key]
}

// AllSlugsInInstance returns the set of slugs currently known for an instance.
// Useful for collision-free slug generation. Read-only.
func (s *MaintainerState) AllSlugsInInstance(quiInstanceID int) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	taken := map[string]bool{}
	for _, r := range s.instanceOrEmptyLocked(quiInstanceID).Rules {
		taken[r.Slug] = true
	}
	return taken
}

// Lookup finds the slug previously assigned to a Qui rule ID. Read-only.
// Returns a copy so callers can't mutate the stored entry.
func (s *MaintainerState) Lookup(quiInstanceID, quiRuleID int) (*RuleStateEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.instanceOrEmptyLocked(quiInstanceID).Rules[strconv.Itoa(quiRuleID)]
	if !ok || r == nil {
		return nil, false
	}
	cp := *r
	return &cp, true
}

// Assign records a slug for a given Qui rule ID.
func (s *MaintainerState) Assign(quiInstanceID, quiRuleID int, slug, name, category string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := s.instanceLocked(quiInstanceID)
	inst.Rules[strconv.Itoa(quiRuleID)] = &RuleStateEntry{
		Slug:     slug,
		LastName: name,
		Category: category,
	}
}

// Forget removes a Qui rule ID from state (used when rule was deleted in Qui).
func (s *MaintainerState) Forget(quiInstanceID, quiRuleID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := s.instanceLocked(quiInstanceID)
	delete(inst.Rules, strconv.Itoa(quiRuleID))
}

// SortedRuleIDs returns the Qui rule IDs for an instance in deterministic order.
// Read-only.
func (s *MaintainerState) SortedRuleIDs(quiInstanceID int) []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst := s.instanceOrEmptyLocked(quiInstanceID)
	ids := make([]int, 0, len(inst.Rules))
	for k := range inst.Rules {
		if n, err := strconv.Atoi(k); err == nil {
			ids = append(ids, n)
		}
	}
	sort.Ints(ids)
	return ids
}

// RenameCategory updates every rule entry in the given instance whose
// category matches oldCat to newCat. Used when the user renames the
// export folder via the UI. Returns the number of entries updated.
func (s *MaintainerState) RenameCategory(quiInstanceID int, oldCat, newCat string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst := s.Instances[strconv.Itoa(quiInstanceID)]
	if inst == nil {
		return 0
	}
	n := 0
	for _, entry := range inst.Rules {
		if entry != nil && entry.Category == oldCat {
			entry.Category = newCat
			n++
		}
	}
	return n
}

// IsExcluded reports whether the user has opted the (instance, rule) pair
// out of export. Read-only.
func (s *MaintainerState) IsExcluded(quiInstanceID, quiRuleID int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst := s.ExcludedRules[strconv.Itoa(quiInstanceID)]
	if inst == nil {
		return false
	}
	return inst[strconv.Itoa(quiRuleID)]
}

// SetExcluded toggles exclusion. Passing false removes the entry so the
// on-disk state stays minimal.
func (s *MaintainerState) SetExcluded(quiInstanceID, quiRuleID int, excluded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ExcludedRules == nil {
		s.ExcludedRules = map[string]map[string]bool{}
	}
	instKey := strconv.Itoa(quiInstanceID)
	ruleKey := strconv.Itoa(quiRuleID)
	if excluded {
		if s.ExcludedRules[instKey] == nil {
			s.ExcludedRules[instKey] = map[string]bool{}
		}
		s.ExcludedRules[instKey][ruleKey] = true
		return
	}
	if s.ExcludedRules[instKey] != nil {
		delete(s.ExcludedRules[instKey], ruleKey)
		if len(s.ExcludedRules[instKey]) == 0 {
			delete(s.ExcludedRules, instKey)
		}
	}
}

// IsInstanceExcluded reports whether an entire instance is opted out of export.
func (s *MaintainerState) IsInstanceExcluded(quiInstanceID int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ExcludedInstances[strconv.Itoa(quiInstanceID)]
}

// SetInstanceExcluded toggles instance-level exclusion.
func (s *MaintainerState) SetInstanceExcluded(quiInstanceID int, excluded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ExcludedInstances == nil {
		s.ExcludedInstances = map[string]bool{}
	}
	key := strconv.Itoa(quiInstanceID)
	if excluded {
		s.ExcludedInstances[key] = true
	} else {
		delete(s.ExcludedInstances, key)
	}
}

// ExcludedSnapshot returns a copy of the excluded-rules map for an instance.
// Useful for UI/API reads without racing on mutations.
func (s *MaintainerState) ExcludedSnapshot(quiInstanceID int) map[int]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[int]bool{}
	src := s.ExcludedRules[strconv.Itoa(quiInstanceID)]
	for k := range src {
		if n, err := strconv.Atoi(k); err == nil {
			out[n] = true
		}
	}
	return out
}

// SortOrderOverride returns (value, true) if the user has set a custom
// sortOrder for this rule, or (0, false) if Qui's value should flow through.
func (s *MaintainerState) SortOrderOverride(quiInstanceID, quiRuleID int) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inst := s.SortOrderOverrides[strconv.Itoa(quiInstanceID)]
	if inst == nil {
		return 0, false
	}
	v, ok := inst[strconv.Itoa(quiRuleID)]
	return v, ok
}

// SetSortOrderOverride sets a custom export-time sortOrder.
func (s *MaintainerState) SetSortOrderOverride(quiInstanceID, quiRuleID, value int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SortOrderOverrides == nil {
		s.SortOrderOverrides = map[string]map[string]int{}
	}
	instKey := strconv.Itoa(quiInstanceID)
	if s.SortOrderOverrides[instKey] == nil {
		s.SortOrderOverrides[instKey] = map[string]int{}
	}
	s.SortOrderOverrides[instKey][strconv.Itoa(quiRuleID)] = value
}

// ClearSortOrderOverride removes a custom sortOrder so Qui's value
// flows through at export time.
func (s *MaintainerState) ClearSortOrderOverride(quiInstanceID, quiRuleID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instKey := strconv.Itoa(quiInstanceID)
	if s.SortOrderOverrides[instKey] == nil {
		return
	}
	delete(s.SortOrderOverrides[instKey], strconv.Itoa(quiRuleID))
	if len(s.SortOrderOverrides[instKey]) == 0 {
		delete(s.SortOrderOverrides, instKey)
	}
}

// RulesSnapshot returns a copy of the rules map for an instance.
// Use this instead of Instance().Rules when iterating outside a lock.
func (s *MaintainerState) RulesSnapshot(quiInstanceID int) map[string]RuleStateEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.instanceOrEmptyLocked(quiInstanceID).Rules
	out := make(map[string]RuleStateEntry, len(src))
	for k, v := range src {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func maintainerStatePath(repoDir string) string {
	return filepath.Join(repoDir, ".qui-sync", "maintainer.state.json")
}
