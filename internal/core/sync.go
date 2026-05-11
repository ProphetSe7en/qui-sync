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
// table, the Removed list, and the Orphans list from this.
type SyncPlan struct {
	SubscriptionSlug string            `json:"subscription_slug"`
	QuiInstanceID    int               `json:"qui_instance_id"`
	SourceSHA        string            `json:"source_sha"` // HEAD SHA of the subscription clone
	Rules            []SyncPlanRule    `json:"rules"`
	Removed          []SyncPlanRemoved `json:"removed"`
	Orphans          []SyncPlanOrphan  `json:"orphans"`
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

// SyncPlanOrphan: a rule that exists in Qui but not in the repo and was
// never part of this subscription (the user's own local rule, or a
// rule the user excluded via "skip"). qui-sync never touches these.
type SyncPlanOrphan struct {
	QuiRuleID int    `json:"qui_rule_id"`
	Name      string `json:"name"`
}

// SyncPlanRemoved: a rule that this subscription previously applied,
// but which the maintainer has now removed from the upstream repo —
// the file is gone from the clone, yet state still records the link to
// a live Qui rule. UI surfaces it in its own section so the user can
// see the auto-deactivate decision (or kick it off manually for rules
// without AutoSync).
//
// Distinct from Orphans, which mix in the user's own private rules
// (never on a subscription) and explicit "skip" decisions. This list
// only contains rules the user once said yes to applying.
type SyncPlanRemoved struct {
	// RepoPath is the stable key in state — "<category>/<slug>".
	RepoPath string `json:"repo_path"`
	// Category preserves the maintainer-side category at the time of
	// the last apply. Useful for UI grouping/display.
	Category string `json:"category"`
	// Slug is the stable identifier the rule used in the repo.
	Slug string `json:"slug"`
	// LastName is the human-readable label shown to the user since
	// the repo file is gone. Backfilled from the live Qui rule when
	// the link is still alive; falls back to the slug when even the
	// live rule has been deleted (v0.5 may add a LastName stash on
	// state so the original name survives both removals).
	LastName string `json:"last_name"`
	// QuiRuleID is the live Qui rule id this state entry links to.
	// 0 means the rule is no longer in Qui either (user deleted it
	// themselves between applies) — UI should treat that as "nothing
	// to deactivate" and offer to clear the state entry.
	QuiRuleID int `json:"qui_rule_id,omitempty"`
	// AutoSync mirrors the AutoSync flag from state — drives the
	// default action shown in UI ("Will deactivate" vs "Manual review").
	AutoSync bool `json:"auto_sync"`
	// AlreadyDeactivated is true when state.DeactivatedBecauseRemoved
	// is set, meaning qui-sync already flipped enabled=false on a prior
	// tick. UI fades the row out and shows "Deactivated" badge.
	AlreadyDeactivated bool `json:"already_deactivated"`
	// CurrentlyEnabled mirrors the live Qui rule's enabled flag at
	// plan-time. Lets the UI distinguish "we already deactivated and
	// the user has not re-enabled" from "we already deactivated but the
	// user re-enabled it later" — the latter is left alone.
	CurrentlyEnabled bool `json:"currently_enabled"`
}

// ---- Apply types (client → server → client) ----

// SyncApplyRequest is what the UI sends when the user clicks "Apply selected".
type SyncApplyRequest struct {
	QuiInstanceID int                 `json:"qui_instance_id"`
	Decisions     []SyncApplyDecision `json:"decisions"`
	// DeactivateRemoved lists repo_paths from the SyncPlan.Removed
	// section that the caller wants to deactivate in Qui this run.
	// Each entry's state must have a valid QuiRuleID — the apply path
	// fetches the live rule, flips enabled=false, and PUTs it back.
	// Auto-sync builds this list from plan.Removed where AutoSync was
	// true and AlreadyDeactivated was false; the manual UI populates
	// it from the user's per-row "Deactivate now" clicks.
	DeactivateRemoved []string `json:"deactivate_removed,omitempty"`
}

type SyncApplyDecision struct {
	RepoPath          string `json:"repo_path"`
	Action            string `json:"action"` // create_new | update_existing | skip
	QuiRuleID         int    `json:"qui_rule_id,omitempty"`
	SortOrderOverride *int   `json:"sort_order_override,omitempty"`
	// AutoSync stages the auto-sync flag from the Plan view so a user
	// who ticks Auto on a brand-new rule has it persist in one shot —
	// Apply both creates the link and records the auto-sync intent.
	// Pre-v0.4 the user had to Apply, then go back and tick Auto on
	// the now-state-backed row. AutoSync defaults false (== unchanged).
	AutoSync bool `json:"auto_sync,omitempty"`
}

// SyncApplyResult is the outcome.
type SyncApplyResult struct {
	Created     []SyncApplyOutcome `json:"created"`
	Updated     []SyncApplyOutcome `json:"updated"`
	Skipped     []SyncApplyOutcome `json:"skipped"`
	Failed      []SyncApplyOutcome `json:"failed"`
	Deactivated []SyncApplyOutcome `json:"deactivated"`
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
				Slug      string `json:"_slug"`
				Name      string `json:"name"`
				SortOrder int    `json:"sortOrder"`
			}
			if err := json.Unmarshal(data, &parsed); err != nil || parsed.Name == "" {
				// Not a rule file (malformed JSON, or lacking the
				// required "name" field). Safely ignored.
				continue
			}
			// Prefer the maintainer-assigned _slug from the file
			// payload — it stays stable across renames on the
			// maintainer side, so a renamed rule maps back to the
			// same subscriber-side state entry (and a same-Qui-rule
			// "update_existing" instead of looking like a delete +
			// create pair). Fall back to Slugify(name) when the
			// field is missing — handles legacy maintainer builds
			// (pre-_slug) and hand-curated rule files that omit it.
			//
			// Migration: existing subscriber state was keyed by
			// Slugify(original_name). Maintainer's _slug equals
			// Slugify(original_name) for non-collision cases (the
			// UniqueSlug seed is the same Slugify call), so the new
			// repo_path matches the old state-key in every common
			// case. Collision-suffixed slugs (rare) will look like
			// a fresh rule to existing subscribers; they get the
			// "Removed by maintainer" + "Create new" pair once,
			// then settle on the content-slug key going forward.
			slug := parsed.Slug
			if slug == "" {
				slug = Slugify(parsed.Name)
			}
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

		// 3) No match — create new. Default AutoSync=true so the Plan
		// UI's Auto checkbox reflects what auto-sync will actually do:
		// a brand-new rule with no state gets auto-created as disabled
		// on the next tick. A user who wants to opt out can untick Auto
		// before clicking Apply (which persists state with AutoSync=false
		// and stops auto-sync from re-creating the rule).
		pr.Action = "create_new"
		pr.AutoSync = true
		pr.LinkSuggestion = SyncPlanSuggestion{MatchType: "none", Confidence: "high"}
		plan.Rules = append(plan.Rules, pr)
	}

	// Removed-by-maintainer: state entries the user once applied (or
	// linked) whose repo_path is no longer present in repoRules. We
	// distinguish these from generic orphans because the user's intent
	// for them is different — they once said "yes, sync this rule",
	// and qui-sync owes them visibility (and optionally an automatic
	// deactivate) on the rule's disappearance.
	//
	// repoRules has already been filtered by TargetCategory above, so
	// the join here is naturally scoped to the subscription's apply
	// scope. State entries for other categories are silently ignored —
	// they don't belong to this subscription's auto-sync surface.
	repoByPath := map[string]bool{}
	for _, rr := range repoRules {
		repoByPath[rr.RepoPath] = true
	}
	for repoPath, entry := range sub.Rules {
		if entry == nil {
			continue
		}
		if repoByPath[repoPath] {
			continue // still in the repo, classified above
		}
		// Skip explicit "skip" decisions — the user already opted out
		// before any apply ran. There's no Qui-side state to manage.
		if entry.LinkedAction == "" || entry.LinkedAction == "skip" {
			continue
		}
		// TargetCategory restricts auto-sync surface; mirror that here
		// so the Removed section doesn't show entries the user won't
		// have actioned anyway. Best-effort string match — Category
		// lives in repoPath as the prefix before "/".
		if sub.TargetCategory != "" {
			slash := strings.IndexByte(repoPath, '/')
			cat := repoPath
			if slash >= 0 {
				cat = repoPath[:slash]
			}
			if cat != sub.TargetCategory {
				continue
			}
		}
		// Skip entries linked to a different Qui instance — the user
		// switched targets at some point. Auto-sync only touches the
		// configured target instance; surfacing other-instance entries
		// would imply we'd act on them, which we won't.
		if entry.QuiInstanceID != 0 && entry.QuiInstanceID != instanceID {
			continue
		}
		// Look up the live Qui state. If the rule no longer exists in
		// Qui (user deleted it themselves), there's nothing left to
		// deactivate — but we still surface it so the UI can offer to
		// clear the now-dangling state entry. Distinguish via QuiRuleID
		// presence + an Already=true marker below.
		var liveEnabled bool
		if live, ok := liveByID[entry.QuiRuleID]; ok {
			liveEnabled = live.Enabled
			// Claim the live rule so the Orphans loop below doesn't
			// also list it. Without this, a maintainer-removed rule
			// shows up in BOTH "Removed by maintainer" AND "Rules
			// only in your Qui" — confusing for the user since both
			// sections describe the same Qui rule from different angles.
			usedLiveIDs[entry.QuiRuleID] = true
		}
		slug := ""
		category := ""
		if slash := strings.IndexByte(repoPath, '/'); slash >= 0 {
			category = repoPath[:slash]
			slug = repoPath[slash+1:]
		} else {
			slug = repoPath
		}
		plan.Removed = append(plan.Removed, SyncPlanRemoved{
			RepoPath:           repoPath,
			Category:           category,
			Slug:               slug,
			LastName:           "", // filled below from live Qui rule when alive, else slug
			QuiRuleID:          entry.QuiRuleID,
			AutoSync:           entry.AutoSync,
			AlreadyDeactivated: entry.DeactivatedBecauseRemoved,
			CurrentlyEnabled:   liveEnabled,
		})
	}
	// Backfill LastName from live Qui rules (we have liveByID; the
	// rule's name comes from there). If the live rule is also gone,
	// fall back to the slug for human readability.
	for i := range plan.Removed {
		if rem := plan.Removed[i]; rem.QuiRuleID > 0 {
			if live, ok := liveByID[rem.QuiRuleID]; ok {
				plan.Removed[i].LastName = live.Name
			}
		}
		if plan.Removed[i].LastName == "" {
			plan.Removed[i].LastName = plan.Removed[i].Slug
		}
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
	sort.Slice(plan.Removed, func(i, j int) bool {
		return plan.Removed[i].RepoPath < plan.Removed[j].RepoPath
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
			// Always persist the auto-sync flag — both true AND false
			// must take effect at Apply time, otherwise unticking Auto
			// on a previously-auto rule via the Plan UI would silently
			// keep the prior true value (the gated `if d.AutoSync` only
			// covered the set-true direction).
			state.SetRuleAutoSync(slug, d.RepoPath, d.AutoSync)
			// Clear the "deactivated by qui-sync" marker — if the user
			// is re-creating a previously-removed rule, the state goes
			// back to "live" so future removals re-trigger the standard
			// Removed flow.
			state.ClearDeactivatedBecauseRemoved(slug, d.RepoPath)
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
			// Always persist (see create_new branch comment).
			state.SetRuleAutoSync(slug, d.RepoPath, d.AutoSync)
			// Clear the "deactivated by qui-sync" marker — see create_new
			// branch. Symmetric: a re-apply of any kind brings the rule
			// back to a normal live state.
			state.ClearDeactivatedBecauseRemoved(slug, d.RepoPath)
			result.Updated = append(result.Updated, out)

		default:
			out.Error = "invalid action: " + d.Action
			result.Failed = append(result.Failed, out)
		}
	}

	// Deactivate-on-removal: rules whose maintainer-side file is gone
	// but the consumer's Qui rule still exists. We flip enabled=false
	// rather than delete — the user can always re-enable manually if
	// they disagree with the maintainer's removal. Each entry's state
	// gets DeactivatedBecauseRemoved=true so subsequent ticks don't
	// keep retrying. Failures are tolerated per-rule.
	if len(req.DeactivateRemoved) > 0 {
		// Snapshot live state once so deactivate doesn't issue N list
		// requests when called from a UI batch.
		freshList, listErr := client.ListAutomations(ctx, req.QuiInstanceID)
		if listErr != nil {
			// A transient Qui API failure here used to silently fall
			// through with an empty freshByID, which made EVERY entry
			// in DeactivateRemoved match the "rule already gone from
			// Qui" branch and get permanently marked as deactivated
			// in state — no flip in Qui, no chance to retry on the
			// next tick. Fail the whole batch instead: each entry is
			// recorded as a per-rule failure with the list error so
			// auto-sync logs it and the marker stays unset for retry.
			for _, repoPath := range req.DeactivateRemoved {
				result.Failed = append(result.Failed, SyncApplyOutcome{
					RepoPath: repoPath,
					Error:    "list automations: " + listErr.Error(),
				})
			}
			if err := state.Save(cfg.Paths().State); err != nil {
				return result, fmt.Errorf("save state: %w", err)
			}
			return result, nil
		}
		freshByID := map[int]Automation{}
		for _, a := range freshList {
			freshByID[a.ID] = a
		}
		subSnap := state.SubscriptionSnapshot(slug)
		for _, repoPath := range req.DeactivateRemoved {
			entry := subSnap.Rules[repoPath]
			out := SyncApplyOutcome{RepoPath: repoPath}
			if entry == nil || entry.QuiRuleID == 0 {
				// No state link or no Qui rule id — nothing to act on,
				// but record state so the UI knows we considered it.
				out.Error = "no Qui rule link in state"
				result.Failed = append(result.Failed, out)
				continue
			}
			out.QuiRuleID = entry.QuiRuleID
			live, ok := freshByID[entry.QuiRuleID]
			if !ok {
				// Rule is gone from Qui already (user deleted manually).
				// Mark deactivated in state so it stops appearing in
				// the Removed section; nothing to flip on Qui's side.
				if !state.MarkDeactivatedBecauseRemoved(slug, repoPath) {
					out.Error = "rule no longer in Qui, state update failed"
					result.Failed = append(result.Failed, out)
					continue
				}
				out.Name = repoPath // best label we have without a live rule
				result.Deactivated = append(result.Deactivated, out)
				continue
			}
			out.Name = live.Name
			if !live.Enabled {
				// Already disabled — record state and move on. No need
				// to issue a no-op PUT.
				state.MarkDeactivatedBecauseRemoved(slug, repoPath)
				result.Deactivated = append(result.Deactivated, out)
				continue
			}
			// Decode raw → flip enabled → PUT.
			var data map[string]any
			if err := json.Unmarshal(live.Raw, &data); err != nil {
				out.Error = "decode rule: " + err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			data["enabled"] = false
			payload, err := json.Marshal(data)
			if err != nil {
				out.Error = "encode rule: " + err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			if err := client.UpdateAutomation(ctx, req.QuiInstanceID, entry.QuiRuleID, payload); err != nil {
				out.Error = err.Error()
				result.Failed = append(result.Failed, out)
				continue
			}
			state.MarkDeactivatedBecauseRemoved(slug, repoPath)
			result.Deactivated = append(result.Deactivated, out)
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
