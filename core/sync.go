package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ---- Plan types (server → client) ----

// SyncPlan is the preview returned by PlanSync. UI renders the Rules
// table and the Orphans list from this.
type SyncPlan struct {
	SubscriptionSlug string           `json:"subscription_slug"`
	QuiInstanceID    int              `json:"qui_instance_id"`
	SourceSHA        string           `json:"source_sha"` // HEAD SHA of the subscription clone
	Rules            []SyncPlanRule   `json:"rules"`
	Orphans          []SyncPlanOrphan `json:"orphans"`
}

// SyncPlanRule describes what Apply would do with one repo rule.
type SyncPlanRule struct {
	// RepoPath: "<category>/<slug>" as stored on disk, relative to rules/.
	RepoPath string `json:"repo_path"`
	Category string `json:"category"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	// Action: "create_new" | "update_existing" | "skip".
	// Derived from state (user's previous decision) or auto-suggested.
	Action string `json:"action"`
	// QuiRuleID is set when Action == update_existing.
	QuiRuleID int `json:"qui_rule_id,omitempty"`
	// LinkSuggestion explains how the auto-match arrived at the action.
	LinkSuggestion SyncPlanSuggestion `json:"link_suggestion"`
	// SortOrder and its source (repo vs user override).
	SortOrder     int    `json:"sort_order"`
	SortOrderFrom string `json:"sort_order_from"` // "repo" | "override"
	// FromState is true if this action was remembered from a previous plan.
	FromState bool `json:"from_state"`
	// AutoSync: true if the rule will be auto-applied on background pulls.
	// Only meaningful when FromState is true (must Apply manually first).
	AutoSync bool `json:"auto_sync"`
}

// SyncPlanSuggestion is the rationale the UI shows for the default pick.
type SyncPlanSuggestion struct {
	MatchType  string `json:"match_type"` // "state" | "name" | "slug" | "none"
	QuiRuleID  int    `json:"qui_rule_id,omitempty"`
	Confidence string `json:"confidence,omitempty"` // "high" | "medium" | "low"
}

// SyncPlanOrphan: a rule that exists in Qui but not in the repo.
// MVP doesn't touch them — v0.3 adds orphan_behavior.
type SyncPlanOrphan struct {
	QuiRuleID int    `json:"qui_rule_id"`
	Name      string `json:"name"`
}

// ---- Apply types (client → server → client) ----

// SyncApplyRequest is what the UI sends when the user clicks "Apply selected".
type SyncApplyRequest struct {
	QuiInstanceID int                 `json:"qui_instance_id"`
	Decisions     []SyncApplyDecision `json:"decisions"`
}

type SyncApplyDecision struct {
	RepoPath          string `json:"repo_path"`
	Action            string `json:"action"` // create_new | update_existing | skip
	QuiRuleID         int    `json:"qui_rule_id,omitempty"`
	SortOrderOverride *int   `json:"sort_order_override,omitempty"`
}

// SyncApplyResult is the outcome.
type SyncApplyResult struct {
	Created []SyncApplyOutcome `json:"created"`
	Updated []SyncApplyOutcome `json:"updated"`
	Skipped []SyncApplyOutcome `json:"skipped"`
	Failed  []SyncApplyOutcome `json:"failed"`
}

type SyncApplyOutcome struct {
	RepoPath  string `json:"repo_path"`
	QuiRuleID int    `json:"qui_rule_id,omitempty"`
	Name      string `json:"name"`
	Error     string `json:"error,omitempty"`
}

// ---- engine ----

// subscriptionLocks serializes every pull/plan/apply for the same
// subscription. One broad lock per slug is simpler and safer than a
// finer per-slug+instance lock — prevents e.g. a Pull from rewriting
// files while Apply is reading them. Different subscriptions still
// run fully in parallel.
var (
	subLocksMu sync.Mutex
	subLocks   = map[string]*sync.Mutex{}
)

func subLockFor(slug string) *sync.Mutex {
	subLocksMu.Lock()
	defer subLocksMu.Unlock()
	m, ok := subLocks[slug]
	if !ok {
		m = &sync.Mutex{}
		subLocks[slug] = m
	}
	return m
}

// SubscriptionCloneDir is the canonical on-disk location of a subscription's
// git clone. Exported so handlers can compute the path identically.
func SubscriptionCloneDir(sourcesDir, slug string) string {
	return filepath.Join(sourcesDir, slug)
}

// PullSubscription fetches/clones the subscription and records the new
// HEAD SHA in state. Idempotent — first call clones, subsequent fetch.
func PullSubscription(ctx context.Context, cfg *Config, state *ConsumerState, slug string) (string, error) {
	lock := subLockFor(slug)
	lock.Lock()
	defer lock.Unlock()

	sub := state.SubscriptionSnapshot(slug)
	if sub == nil {
		return "", fmt.Errorf("subscription %q not found", slug)
	}
	auth := GitAuth{Mode: sub.Auth.Mode, KeyFile: sub.Auth.KeyFile}
	if sub.Auth.Mode == GitAuthToken && sub.Auth.TokenFile != "" {
		tok, err := os.ReadFile(sub.Auth.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file: %w", err)
		}
		auth.Token = strings.TrimSpace(string(tok))
	}

	dir := SubscriptionCloneDir(cfg.Paths().Sources, slug)
	var newSHA string
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := CloneSubscription(ctx, sub.URL, sub.Branch, dir, auth); err != nil {
			return "", err
		}
		sha, err := CurrentSHA(ctx, dir)
		if err != nil {
			return "", err
		}
		newSHA = sha
	} else {
		sha, err := FetchSubscription(ctx, dir, sub.Branch, auth)
		if err != nil {
			return "", err
		}
		newSHA = sha
	}
	state.RecordPull(slug, newSHA)
	if err := state.Save(cfg.Paths().State); err != nil {
		return "", err
	}
	return newSHA, nil
}

// repoRule is a parsed rule file from a subscription clone.
type repoRule struct {
	RepoPath  string // "<category>/<slug>"
	Category  string
	Slug      string
	Name      string
	SortOrder int
	Raw       []byte // full JSON as on disk
}

// listRepoRules walks a subscription clone and parses every rule file.
// Rule files live at "<category>/<filename>.json" and are identified by
// their top-level "name" field; anything else in the repo (LICENSE,
// README, scripts/, docs/) is skipped automatically.
func listRepoRules(cloneDir string) ([]repoRule, error) {
	entries, err := os.ReadDir(cloneDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []repoRule
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		// archive/ holds rules the maintainer has retired — not current
		// rules. Consumers never apply them to their Qui.
		if entry.Name() == "archive" {
			continue
		}
		cat := entry.Name()
		catDir := filepath.Join(cloneDir, cat)
		files, err := os.ReadDir(catDir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			full := filepath.Join(catDir, f.Name())
			data, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			var parsed struct {
				Name      string `json:"name"`
				SortOrder int    `json:"sortOrder"`
			}
			if err := json.Unmarshal(data, &parsed); err != nil || parsed.Name == "" {
				// Not a rule file (malformed JSON, or lacking the
				// required "name" field). Safely ignored.
				continue
			}
			// Slug identity is derived from the rule name so state
			// keys stay stable regardless of filesystem quirks in
			// the filename.
			slug := Slugify(parsed.Name)
			out = append(out, repoRule{
				RepoPath:  cat + "/" + slug,
				Category:  cat,
				Slug:      slug,
				Name:      parsed.Name,
				SortOrder: parsed.SortOrder,
				Raw:       data,
			})
		}
	}
	return out, nil
}

// PlanSync builds the plan by walking the cloned repo and joining against
// live Qui rules + previous state decisions.
func PlanSync(ctx context.Context, cfg *Config, client *QuiClient, state *ConsumerState, slug string, instanceID int) (*SyncPlan, error) {
	lock := subLockFor(slug)
	lock.Lock()
	defer lock.Unlock()

	sub := state.SubscriptionSnapshot(slug)
	if sub == nil {
		return nil, fmt.Errorf("subscription %q not found", slug)
	}
	dir := SubscriptionCloneDir(cfg.Paths().Sources, slug)
	sha, err := CurrentSHA(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("no clone for subscription — run Pull first: %w", err)
	}

	repoRules, err := listRepoRules(dir)
	if err != nil {
		return nil, fmt.Errorf("list repo rules: %w", err)
	}

	// Per-subscription category filter. Empty TargetCategory means "all
	// categories"; a set value restricts Plan/Apply to rules in that one
	// folder. Mirrors the auto-sync filter so manual + automated runs
	// agree on scope.
	if sub.TargetCategory != "" {
		filtered := repoRules[:0]
		for _, rr := range repoRules {
			if rr.Category == sub.TargetCategory {
				filtered = append(filtered, rr)
			}
		}
		repoRules = filtered
	}

	liveRules, err := client.ListAutomations(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("list live automations: %w", err)
	}
	liveByID := map[int]Automation{}
	liveByName := map[string]Automation{}
	for _, r := range liveRules {
		liveByID[r.ID] = r
		liveByName[strings.TrimSpace(r.Name)] = r
	}

	plan := &SyncPlan{
		SubscriptionSlug: slug,
		QuiInstanceID:    instanceID,
		SourceSHA:        sha,
	}
	usedLiveIDs := map[int]bool{}

	for _, rr := range repoRules {
		pr := SyncPlanRule{
			RepoPath:      rr.RepoPath,
			Category:      rr.Category,
			Slug:          rr.Slug,
			Name:          rr.Name,
			SortOrder:     rr.SortOrder,
			SortOrderFrom: "repo",
		}

		entry := sub.Rules[rr.RepoPath]

		// 1) State remembers a decision from last plan/apply (includes skip).
		if entry != nil && entry.LinkedAction != "" && entry.QuiInstanceID == instanceID {
			pr.Action = entry.LinkedAction
			pr.QuiRuleID = entry.QuiRuleID
			pr.LinkSuggestion = SyncPlanSuggestion{
				MatchType:  "state",
				QuiRuleID:  entry.QuiRuleID,
				Confidence: "high",
			}
			pr.FromState = true
			pr.AutoSync = entry.AutoSync
			if entry.SortOrderOverride != nil {
				pr.SortOrder = *entry.SortOrderOverride
				pr.SortOrderFrom = "override"
			}
			if entry.QuiRuleID > 0 {
				usedLiveIDs[entry.QuiRuleID] = true
			}
			plan.Rules = append(plan.Rules, pr)
			continue
		}

		// 2) Auto-match by exact name against live Qui rules.
		if live, ok := liveByName[strings.TrimSpace(rr.Name)]; ok && !usedLiveIDs[live.ID] {
			pr.Action = "update_existing"
			pr.QuiRuleID = live.ID
			pr.LinkSuggestion = SyncPlanSuggestion{
				MatchType:  "name",
				QuiRuleID:  live.ID,
				Confidence: "medium",
			}
			usedLiveIDs[live.ID] = true
			plan.Rules = append(plan.Rules, pr)
			continue
		}

		// 3) No match — create new.
		pr.Action = "create_new"
		pr.LinkSuggestion = SyncPlanSuggestion{MatchType: "none", Confidence: "high"}
		plan.Rules = append(plan.Rules, pr)
	}

	// Orphans: live rules not claimed by any repo rule.
	for _, live := range liveRules {
		if !usedLiveIDs[live.ID] {
			plan.Orphans = append(plan.Orphans, SyncPlanOrphan{
				QuiRuleID: live.ID,
				Name:      live.Name,
			})
		}
	}

	// Deterministic order for the UI.
	sort.Slice(plan.Rules, func(i, j int) bool {
		if plan.Rules[i].SortOrder != plan.Rules[j].SortOrder {
			return plan.Rules[i].SortOrder < plan.Rules[j].SortOrder
		}
		return plan.Rules[i].RepoPath < plan.Rules[j].RepoPath
	})
	sort.Slice(plan.Orphans, func(i, j int) bool {
		return plan.Orphans[i].Name < plan.Orphans[j].Name
	})

	return plan, nil
}

// ApplySync executes the decisions. Returns a per-rule outcome breakdown.
// Partial failures are tolerated — each rule succeeds or fails independently.
func ApplySync(ctx context.Context, cfg *Config, client *QuiClient, state *ConsumerState, slug string, req SyncApplyRequest) (*SyncApplyResult, error) {
	lock := subLockFor(slug)
	lock.Lock()
	defer lock.Unlock()

	if state.SubscriptionSnapshot(slug) == nil {
		return nil, fmt.Errorf("subscription %q not found", slug)
	}

	dir := SubscriptionCloneDir(cfg.Paths().Sources, slug)
	repoRules, err := listRepoRules(dir)
	if err != nil {
		return nil, fmt.Errorf("list repo rules: %w", err)
	}
	repoByPath := map[string]repoRule{}
	for _, rr := range repoRules {
		repoByPath[rr.RepoPath] = rr
	}

	// Qui's API exposes only the list endpoint (GET /api/instances/{id}
	// /automations) — there is no single-rule GET. Fetch the whole list
	// once at apply-start and index by rule ID so every "update_existing"
	// decision gets O(1) live-state lookup without issuing per-rule
	// requests (which would all come back as HTTP 405 Method Not Allowed).
	liveList, err := client.ListAutomations(ctx, req.QuiInstanceID)
	if err != nil {
		return nil, fmt.Errorf("list live automations: %w", err)
	}
	liveByID := make(map[int]json.RawMessage, len(liveList))
	for _, a := range liveList {
		liveByID[a.ID] = a.Raw
	}

	result := &SyncApplyResult{}

	for _, d := range req.Decisions {
		rr, ok := repoByPath[d.RepoPath]
		if !ok {
			result.Failed = append(result.Failed, SyncApplyOutcome{
				RepoPath: d.RepoPath,
				Error:    "not found in repo clone",
			})
			continue
		}

		out := SyncApplyOutcome{RepoPath: d.RepoPath, Name: rr.Name}

		switch d.Action {
		case "skip":
			// Remember the skip for future plans.
			state.SetRuleDecision(slug, d.RepoPath, req.QuiInstanceID, 0, "skip", nil)
			out.QuiRuleID = 0
			result.Skipped = append(result.Skipped, out)

		case "create_new":
			payload, err := buildCreatePayload(rr, d.SortOrderOverride)
			if err != nil {
				out.Error = err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			ruleID, err := client.CreateAutomation(ctx, req.QuiInstanceID, payload)
			if err != nil {
				out.Error = err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			out.QuiRuleID = ruleID
			state.SetRuleDecision(slug, d.RepoPath, req.QuiInstanceID, ruleID, "update_existing", d.SortOrderOverride)
			state.RecordApply(slug, d.RepoPath, ruleID, sha256String(rr.Raw))
			result.Created = append(result.Created, out)

		case "update_existing":
			if d.QuiRuleID == 0 {
				out.Error = "qui_rule_id required for update_existing"
				result.Failed = append(result.Failed, out)
				continue
			}
			liveRaw, ok := liveByID[d.QuiRuleID]
			if !ok {
				out.QuiRuleID = d.QuiRuleID
				out.Error = fmt.Sprintf("rule #%d no longer exists in Qui instance %d — it may have been deleted in Qui since the plan was built", d.QuiRuleID, req.QuiInstanceID)
				result.Failed = append(result.Failed, out)
				continue
			}
			entry := state.SubscriptionSnapshot(slug).Rules[d.RepoPath]
			preserve := DefaultPreservedFields()
			if entry != nil && len(entry.PreservedFields) > 0 {
				preserve = entry.PreservedFields
			}
			payload, err := buildUpdatePayload(rr, liveRaw, preserve, d.SortOrderOverride)
			if err != nil {
				out.QuiRuleID = d.QuiRuleID
				out.Error = err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			if err := client.UpdateAutomation(ctx, req.QuiInstanceID, d.QuiRuleID, payload); err != nil {
				out.QuiRuleID = d.QuiRuleID
				out.Error = err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			out.QuiRuleID = d.QuiRuleID
			state.SetRuleDecision(slug, d.RepoPath, req.QuiInstanceID, d.QuiRuleID, "update_existing", d.SortOrderOverride)
			state.RecordApply(slug, d.RepoPath, d.QuiRuleID, sha256String(rr.Raw))
			result.Updated = append(result.Updated, out)

		default:
			out.Error = "invalid action: " + d.Action
			result.Failed = append(result.Failed, out)
		}
	}

	if err := state.Save(cfg.Paths().State); err != nil {
		return result, fmt.Errorf("save state: %w", err)
	}
	return result, nil
}

// ---- payload builders ----

// buildCreatePayload starts from the repo JSON, strips server-assigned
// identity/timestamp fields, applies defaults for user-owned fields
// (enabled: false per design), and applies the user's sort-order override.
// Trackers and intervals are left absent — Qui fills in its own defaults.
func buildCreatePayload(rr repoRule, sortOverride *int) (json.RawMessage, error) {
	var obj map[string]any
	if err := json.Unmarshal(rr.Raw, &obj); err != nil {
		return nil, fmt.Errorf("parse repo rule: %w", err)
	}
	// Strip fields the server assigns.
	for _, k := range []string{"id", "instanceId", "_slug", "_description", "createdAt", "updatedAt"} {
		delete(obj, k)
	}
	// Start all created rules disabled so the user reviews in Qui first.
	obj["enabled"] = false
	if sortOverride != nil {
		obj["sortOrder"] = *sortOverride
	}
	return json.Marshal(obj)
}

// buildUpdatePayload is the 3-layer merge:
//  1. repo_rule (logic: name, conditions, sortOrder-default)
//  2. Preserve listed fields from live_rule (user's trackers, enabled, etc.)
//  3. sort_order_override from state/request wins over repo+live
func buildUpdatePayload(rr repoRule, liveRaw json.RawMessage, preserveFields []string, sortOverride *int) (json.RawMessage, error) {
	var merged map[string]any
	if err := json.Unmarshal(rr.Raw, &merged); err != nil {
		return nil, fmt.Errorf("parse repo rule: %w", err)
	}
	var live map[string]any
	if err := json.Unmarshal(liveRaw, &live); err != nil {
		return nil, fmt.Errorf("parse live rule: %w", err)
	}

	// Preserve user-owned fields from live.
	for _, f := range preserveFields {
		if v, ok := live[f]; ok {
			merged[f] = v
		} else {
			// Live doesn't have it — drop from payload so Qui keeps its default.
			delete(merged, f)
		}
	}

	// Strip server-assigned / client-only fields regardless.
	for _, k := range []string{"id", "instanceId", "_slug", "_description", "createdAt", "updatedAt"} {
		delete(merged, k)
	}

	// Final sort-order override wins.
	if sortOverride != nil {
		merged["sortOrder"] = *sortOverride
	}

	return json.Marshal(merged)
}

// ---- helpers ----

// sha256String computes a hex SHA256 of arbitrary bytes. Used to stamp
// applied_sha on a rule entry so v0.3 drift detection has something to
// compare against.
func sha256String(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
