package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"github.com/prophetse7en/qui-sync/internal/core"
	"github.com/prophetse7en/qui-sync/internal/core/customize"
)

// Customize endpoints — Phase 1.2 of the personal-customizations
// feature. See dev/docs/personal-overrides-spec.md for design.
//
// Four endpoints implement the spec's three user flows:
//   /setup-diff   — setup-time auto-detect (modal at subscription mapping)
//   /capture      — post-setup Detect-changes button
//   /reset        — drop a stored customization
//   /diff         — fetch stored diff for the conflict UI

// customizeDiffSummary is the JSON shape returned by setup-diff and
// capture. has_changes drives the UI's "show modal" branch; diff +
// fragile_ops let the caller render the human-readable summary.
type customizeDiffSummary struct {
	HasChanges  bool            `json:"has_changes"`
	Diff        json.RawMessage `json:"diff,omitempty"`
	FragileOps  []string        `json:"fragile_ops,omitempty"`
	SchemaVer   string          `json:"schema_version,omitempty"`
	UpstreamSHA string          `json:"upstream_sha,omitempty"`
}

// handleSetupDiff is called at subscription-mapping time, before the
// mapping is persisted to consumer state. Given the upstream rule
// file (sub + repo_path) and the live Qui rule the user picked
// (qui_instance_id + qui_rule_id), compute the diff so the UI can
// show the "your rule differs from upstream" modal with Keep / Reset
// options. NO customization is saved here — that's /capture's job.
//
// Returns 200 with has_changes=false when the live rule already
// matches upstream (no modal needed; setup proceeds clean).
func (s *Server) handleSetupDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subscription  string `json:"subscription"`
		RepoPath      string `json:"repo_path"`
		QuiInstanceID int    `json:"qui_instance_id"`
		QuiRuleID     int    `json:"qui_rule_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Subscription == "" || req.RepoPath == "" || req.QuiInstanceID <= 0 || req.QuiRuleID <= 0 {
		writeErr(w, 400, fmt.Errorf("subscription, repo_path, qui_instance_id, qui_rule_id are required"))
		return
	}

	upstream, live, _, err := s.loadUpstreamAndLive(r, req.Subscription, req.RepoPath, req.QuiInstanceID, req.QuiRuleID)
	if err != nil {
		writeErr(w, errCodeFor(err), err)
		return
	}

	summary, err := computeDiffSummary(upstream, live)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, summary)
}

// handleCaptureCustomization is the Detect-changes endpoint on the
// Customize toggle's "capture" action. Same diff logic as setup-diff
// PLUS persists the resulting customization to disk. Idempotent on
// repeat calls (latest capture wins).
//
// Returns 200 with the computed summary; the saved customization
// applies on the next ApplySync for the bound rule.
//
// The per-(sub, slug) save lock is acquired BEFORE loading upstream +
// live, then held through the entire compute-then-save sequence.
// Pre-fix this only wrapped Save itself, leaving a window where two
// concurrent captures could both read live state, both compute
// diffs, and the second's save would clobber the first with whatever
// live snapshot it happened to read (review B3). The wider scope
// ensures one capture finishes start-to-end before the next starts.
const notesMaxLen = 1024

func (s *Server) handleCaptureCustomization(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subscription  string `json:"subscription"`
		RepoPath      string `json:"repo_path"`
		QuiInstanceID int    `json:"qui_instance_id"`
		QuiRuleID     int    `json:"qui_rule_id"`
		CapturedFrom  string `json:"captured_from"`
		Notes         string `json:"notes,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Subscription == "" || req.RepoPath == "" || req.QuiInstanceID <= 0 || req.QuiRuleID <= 0 {
		writeErr(w, 400, fmt.Errorf("subscription, repo_path, qui_instance_id, qui_rule_id are required"))
		return
	}
	if len(req.Notes) > notesMaxLen {
		writeErr(w, 400, fmt.Errorf("notes too long (max %d chars)", notesMaxLen))
		return
	}
	cf := customize.CapturedFrom(req.CapturedFrom)
	switch cf {
	case customize.CapturedFromSetupTime, customize.CapturedFromPostSetup:
	default:
		writeErr(w, 400, fmt.Errorf("invalid captured_from %q (must be %q or %q)", req.CapturedFrom, customize.CapturedFromSetupTime, customize.CapturedFromPostSetup))
		return
	}

	// Eager peek at the rule slug for lock-key — we don't know it
	// without reading the upstream file once. Acquire the lock as
	// soon as we have it (which is before we make any Qui call OR
	// touch storage), so the compute-then-save window is fully
	// serialised on the (sub, slug) key.
	upstream, live, ruleSlug, err := s.loadUpstreamAndLive(r, req.Subscription, req.RepoPath, req.QuiInstanceID, req.QuiRuleID)
	if err != nil {
		writeErr(w, errCodeFor(err), err)
		return
	}
	if ruleSlug == "" {
		writeErr(w, 422, fmt.Errorf("upstream rule has no _slug — cannot key customization storage"))
		return
	}

	// Lock now covers compute + save. Even if a concurrent capture
	// raced through loadUpstreamAndLive ahead of us, only one
	// goroutine can be in this critical section at a time per (sub,
	// slug) — so the live snapshot we Capture against is the same
	// one we save against, and our save can't clobber a never-seen
	// concurrent write.
	mu := customize.SaveLockFor(req.Subscription, ruleSlug)
	mu.Lock()
	defer mu.Unlock()

	cfg := s.getConfig()
	c, err := customize.Capture(upstream, live, cf, req.Notes, customize.DefaultCaptureOptions())
	if err == customize.ErrNoChanges {
		// Idempotent zero-diff capture: caller said "save my changes"
		// but there ARE no changes. Treat as success — return summary
		// with has_changes=false and DON'T touch storage (don't
		// silently delete an existing customization either, since the
		// user might have meant to capture against a different live
		// state and the no-changes is a glitch).
		writeJSON(w, 200, customizeDiffSummary{HasChanges: false})
		return
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// We already hold the per-key lock — call lock-free Save directly.
	if err := customize.Save(cfg.Paths().State, req.Subscription, ruleSlug, c); err != nil {
		writeErr(w, 500, fmt.Errorf("save customization: %w", err))
		return
	}
	writeJSON(w, 200, customizeDiffSummary{
		HasChanges:  true,
		Diff:        c.Diff,
		FragileOps:  c.FragileOps,
		SchemaVer:   c.SchemaVersionAssumed,
		UpstreamSHA: c.BaseSHA,
	})
}

// handleSetRuleCustomizing flips the per-rule Customize toggle.
// Toggle ON: rule's Customizing flag becomes true; UI starts showing
// the Detect-changes button + Open-in-Qui deep-link. No storage
// changes yet — the customization file appears only when the user
// runs /capture.
//
// Toggle OFF: per spec Q9 ("Toggle OFF deletes customization"), the
// stored customization (if any) is deleted as a side effect. State
// flag goes false. Next ApplySync applies clean upstream (Layer 3
// still preserves auto-managed fields, as today).
//
// This is keyed by (sub, repo_path) to match the existing
// handleSetRuleAutoSync pattern — both flags live on the same
// SubRuleStateData entry, addressed the same way. The customize
// storage side keys by (sub, rule_slug) — handler reads the slug
// from the rule's state entry, or from the upstream rule file if
// the entry doesn't carry it yet.
func (s *Server) handleSetRuleCustomizing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath    string `json:"repo_path"`
		Customizing bool   `json:"customizing"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.RepoPath == "" {
		writeErr(w, 400, fmt.Errorf("repo_path is required"))
		return
	}
	sub := r.PathValue("sub")
	cfg := s.getConfig()
	state := s.getConsumerState()

	// Verify the rule exists in state BEFORE doing any cascade work —
	// no point deleting a customization file for a rule we're about
	// to 404 on.
	snap := state.SubscriptionSnapshot(sub)
	if snap == nil || snap.Rules[req.RepoPath] == nil {
		writeErr(w, 404, fmt.Errorf("rule %q not found in subscription %q — run Apply first", req.RepoPath, sub))
		return
	}

	// Toggle-OFF cascade: delete the stored customization file FIRST
	// (before flipping the state flag). Per review B1+B3: doing the
	// cascade first means a delete-failure leaves both the file AND
	// the state flag in the previous-true state — eventually-consistent
	// and recoverable (user retries toggle-off). The previous order
	// (state-save then cascade) could leave the flag false + file
	// orphaned, which the UI has no affordance to clean up.
	if !req.Customizing {
		ruleSlug, ok := s.slugForRule(sub, req.RepoPath)
		if !ok {
			// Couldn't determine the slug (upstream file missing or
			// no _slug field). State-only toggle is still safe — the
			// next /capture or /reset will sort the storage side.
			// Don't fail the toggle; log via response.
			log.Printf("customize toggle: skipping cascade for %s/%s — upstream slug not resolvable", sub, req.RepoPath)
		} else {
			if err := customize.DeleteLocked(cfg.Paths().State, sub, ruleSlug); err != nil {
				if errors.Is(err, customize.ErrInvalidIdentifier) {
					// Slug from upstream is malformed (shouldn't
					// happen if Slugify produced it, but defense-in-
					// depth). Treat as unrecoverable storage issue.
					writeErr(w, 500, fmt.Errorf("customization slug invalid; refuse to flip state with stale file: %w", err))
					return
				}
				// Real storage failure (disk wedged, perms, etc.).
				// Refuse the toggle — the user can retry once the
				// underlying issue clears. UI gets the error reason.
				writeErr(w, 500, fmt.Errorf("delete customization: %w", err))
				return
			}
		}
	}

	// Cascade succeeded (or was unnecessary) — flip the state flag.
	if ok := state.SetRuleCustomizing(sub, req.RepoPath, req.Customizing); !ok {
		writeErr(w, 404, fmt.Errorf("rule %q vanished from state between checks — race with another writer?", req.RepoPath))
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		// State save failed AFTER cascade succeeded — the file is
		// gone but the flag still says customizing=true. Acceptable:
		// next Plan shows customizing=true + has_customization=false
		// ("Customize ON, no diff yet"), which the user can recover
		// from by clicking Detect changes. Better than the inverse
		// (flag false + orphaned file).
		writeErr(w, 500, fmt.Errorf("save state: %w", err))
		return
	}

	writeJSON(w, 200, map[string]any{"repo_path": req.RepoPath, "customizing": req.Customizing})
}

// slugForRule resolves the maintainer-assigned _slug for an upstream
// rule given (sub, repoPath). Wraps LoadRepoRuleByPath so the toggle-
// off cascade goes through the same canonical lookup as the customize
// handlers — no second copy of the category/slug/filename mapping.
func (s *Server) slugForRule(sub, repoPath string) (string, bool) {
	cfg := s.getConfig()
	_, slug, notFound, err := core.LoadRepoRuleByPath(cfg, sub, repoPath)
	if err != nil || notFound || slug == "" {
		return "", false
	}
	return slug, true
}

// handleResetCustomization deletes the stored customization for
// (sub, slug). Idempotent — missing customization returns 204 with
// no error (already in the desired state). The actual revert to
// upstream behaviour happens on the next ApplySync.
func (s *Server) handleResetCustomization(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	slug := r.PathValue("slug")
	if sub == "" || slug == "" {
		writeErr(w, 400, fmt.Errorf("sub and slug path parameters are required"))
		return
	}
	cfg := s.getConfig()
	if err := customize.DeleteLocked(cfg.Paths().State, sub, slug); err != nil {
		if errors.Is(err, customize.ErrInvalidIdentifier) {
			writeErr(w, 400, err)
			return
		}
		writeErr(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

// handleGetCustomizationDiff returns the stored customization JSON
// for (sub, slug). 404 when none stored. Used by the conflict UI to
// render the captured diff alongside the upstream-vs-live 3-way diff.
func (s *Server) handleGetCustomizationDiff(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	slug := r.PathValue("slug")
	if sub == "" || slug == "" {
		writeErr(w, 400, fmt.Errorf("sub and slug path parameters are required"))
		return
	}
	cfg := s.getConfig()
	c, err := customize.Load(cfg.Paths().State, sub, slug)
	if err != nil {
		// Differentiate user-input bugs (invalid identifier) from
		// data-integrity issues (corrupt or future-format file) so
		// the UI can surface the right copy. Both still block the
		// regular Load-and-render path, but the version-mismatch
		// case should route to the conflict UI's "Re-capture or
		// Drop" affordance (per review C7/C8).
		if errors.Is(err, customize.ErrInvalidIdentifier) {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"unreadable":    true,
			"error_message": err.Error(),
		})
		return
	}
	if c == nil {
		writeErr(w, 404, fmt.Errorf("no customization for (%s, %s)", sub, slug))
		return
	}
	writeJSON(w, 200, c)
}

// ---- shared helpers ----

// loadUpstreamAndLive resolves upstream + live raw bytes for a
// (subscription, repoPath, qui rule) triple. Returns the rule's
// `_slug` field (the storage key for customizations) alongside.
// All four error returns map to distinct HTTP codes via errCodeFor
// — 404 for missing rule, 502 for Qui failures, 500 for unexpected.
//
// repoPath comes from the caller (HTTP body) — must be constrained
// to point inside the subscription's clone directory. A naive
// filepath.Join with `../../etc/passwd` would let the response leak
// arbitrary file contents (or a 404-message containing the resolved
// path). safeJoinUnderClone enforces the boundary.
func (s *Server) loadUpstreamAndLive(r *http.Request, sub, repoPath string, instID, ruleID int) (upstream, live []byte, ruleSlug string, err error) {
	cfg := s.getConfig()

	// Pre-validate that repo_path doesn't try to escape the clone
	// dir. LoadRepoRuleByPath itself doesn't navigate paths (it walks
	// listRepoRules + matches by exact RepoPath string), but the
	// safety check is cheap and keeps the malicious-input branch
	// loud rather than silent (review B1 from Phase 1.2).
	cloneDir := core.SubscriptionCloneDir(cfg.Paths().Sources, sub)
	if _, ok := safeJoinUnderClone(cloneDir, repoPath); !ok {
		return nil, nil, "", badRequestErr{msg: fmt.Sprintf("repo_path %q escapes the subscription clone directory", repoPath)}
	}

	// LoadRepoRuleByPath handles the category/slug-vs-filename mapping
	// — repo_path is "<category>/<slug>" but the on-disk filename
	// derives from the rule NAME (RuleFilename), not the slug. The
	// helper walks listRepoRules and matches by canonical RepoPath,
	// returning the raw bytes + the maintainer-assigned _slug.
	upstream, ruleSlug, notFound, err := core.LoadRepoRuleByPath(cfg, sub, repoPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("walk repo rules: %w", err)
	}
	if notFound {
		return nil, nil, "", notFoundErr{msg: fmt.Sprintf("upstream rule not found: %s", repoPath)}
	}

	client, err := s.newClient()
	if err != nil {
		return nil, nil, "", err
	}
	rules, err := client.ListAutomations(r.Context(), instID)
	if err != nil {
		return nil, nil, "", badGatewayErr{msg: fmt.Sprintf("list Qui automations: %v", err)}
	}
	for _, rule := range rules {
		if rule.ID == ruleID {
			live = rule.Raw
			break
		}
	}
	if live == nil {
		return nil, nil, "", notFoundErr{msg: fmt.Sprintf("Qui rule %d not found on instance %d", ruleID, instID)}
	}
	return upstream, live, ruleSlug, nil
}

// computeDiffSummary runs Capture with default options on a pristine
// upstream/live pair (no persistence) and returns the JSON-API shape
// the UI consumes. ErrNoChanges becomes has_changes=false; any other
// error bubbles up.
func computeDiffSummary(upstream, live []byte) (*customizeDiffSummary, error) {
	c, err := customize.Capture(upstream, live, customize.CapturedFromSetupTime, "", customize.DefaultCaptureOptions())
	if err == customize.ErrNoChanges {
		return &customizeDiffSummary{HasChanges: false}, nil
	}
	if err != nil {
		return nil, err
	}
	return &customizeDiffSummary{
		HasChanges:  true,
		Diff:        c.Diff,
		FragileOps:  c.FragileOps,
		SchemaVer:   c.SchemaVersionAssumed,
		UpstreamSHA: c.BaseSHA,
	}, nil
}

// notFoundErr + badGatewayErr + badRequestErr are typed error sentinels
// so errCodeFor can map them to specific HTTP status codes without
// string sniffing.
type notFoundErr struct{ msg string }

func (e notFoundErr) Error() string { return e.msg }

type badGatewayErr struct{ msg string }

func (e badGatewayErr) Error() string { return e.msg }

type badRequestErr struct{ msg string }

func (e badRequestErr) Error() string { return e.msg }

// errCodeFor maps the sentinel error types to HTTP status codes. Any
// non-sentinel error becomes 500 — the caller already wrapped it with
// context.
func errCodeFor(err error) int {
	switch err.(type) {
	case notFoundErr:
		return 404
	case badGatewayErr:
		return 502
	case badRequestErr:
		return 400
	default:
		return 500
	}
}

// safeJoinUnderClone joins a caller-supplied repoPath onto cloneDir
// and verifies the result stays under cloneDir. Returns ok=false on
// any traversal attempt (`..`-segments that escape the clone, absolute
// repoPath, symlinks pointing outside — though symlink resolution is
// the OS's problem at read time, not ours here).
//
// Uses filepath.Rel as the canonical check: a path is under cloneDir
// iff the relative path from cloneDir doesn't start with `..` and
// isn't an absolute path on its own.
func safeJoinUnderClone(cloneDir, repoPath string) (string, bool) {
	if cloneDir == "" || repoPath == "" {
		return "", false
	}
	joined := filepath.Join(cloneDir, repoPath)
	rel, err := filepath.Rel(cloneDir, joined)
	if err != nil {
		return "", false
	}
	// rel must be a non-escaping, non-absolute relative path.
	if rel == "." || rel == ".." || rel == "" {
		return "", false
	}
	if filepath.IsAbs(rel) {
		return "", false
	}
	// Reject any rel path that starts with ".." (escapes upward).
	// filepath.Clean normalises "a/../../b" → "../b" so this single
	// check catches every variant.
	if len(rel) >= 2 && rel[0] == '.' && rel[1] == '.' {
		return "", false
	}
	return joined, true
}
