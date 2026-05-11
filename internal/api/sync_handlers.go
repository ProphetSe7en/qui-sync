package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prophetse7en/qui-sync/internal/core"
	"github.com/prophetse7en/qui-sync/internal/netsec"
	"golang.org/x/crypto/ssh"
)

// sub-slugs use the same charset as category/folder names — filesystem
// safe, GitHub compatible.
var subSlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// gitKeyDir builds the canonical directory for a subscription's auth keys.
// Used by both generate-key and add-subscription so the paths stay consistent.
func (s *Server) gitKeyDir(slug string) string {
	return filepath.Join(filepath.Dir(s.cfgPath), "git-keys", slug)
}

// ---- subscriptions CRUD ----

type subscriptionSummary struct {
	Slug             string   `json:"slug"`
	URL              string   `json:"url"`
	Branch           string   `json:"branch"`
	AuthMode         string   `json:"auth_mode"`
	TargetCategory   string   `json:"target_category,omitempty"`
	TargetInstanceID int      `json:"target_instance_id,omitempty"` // pre-bound Qui instance for Plan/Apply (0 = unset)
	RepoCategories   []string `json:"repo_categories,omitempty"`    // top-level dirs in the clone (for the edit-modal dropdown)
	LastPullSHA      string   `json:"last_pull_sha,omitempty"`
	LastPullAt       string   `json:"last_pull_at,omitempty"`
	RuleCount        int      `json:"rule_count"`      // rules with decisions in state
	RepoRuleCount    int      `json:"repo_rule_count"` // rules found in the cloned repo
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	state := s.getConsumerState()
	cfg := s.getConfig()
	out := []subscriptionSummary{}
	for _, slug := range state.ListSubscriptionSlugs() {
		sub := state.SubscriptionSnapshot(slug)
		if sub == nil {
			continue
		}
		sum := subscriptionSummary{
			Slug:             slug,
			URL:              sub.URL,
			Branch:           sub.Branch,
			AuthMode:         string(sub.Auth.Mode),
			TargetCategory:   sub.TargetCategory,
			TargetInstanceID: sub.TargetInstanceID,
			LastPullSHA:      sub.LastPullSHA,
			RuleCount:        len(sub.Rules),
		}
		// Count rules + collect categories from the cloned repo. The
		// repo layout is <category>/<Rule Name>.json at root (the legacy
		// `rules/<category>/` layout is no longer used — see PROJECT.md
		// "single file-layout convention" 2026-04-19).
		cloneDir := core.SubscriptionCloneDir(cfg.Paths().Sources, slug)
		if entries, err := os.ReadDir(cloneDir); err == nil {
			for _, cat := range entries {
				if !cat.IsDir() || strings.HasPrefix(cat.Name(), ".") || cat.Name() == "archive" {
					continue
				}
				files, err := os.ReadDir(filepath.Join(cloneDir, cat.Name()))
				if err != nil {
					continue
				}
				ruleCount := 0
				for _, f := range files {
					if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
						ruleCount++
					}
				}
				if ruleCount > 0 {
					sum.RepoRuleCount += ruleCount
					sum.RepoCategories = append(sum.RepoCategories, cat.Name())
				}
			}
		}
		if !sub.LastPullAt.IsZero() {
			sum.LastPullAt = sub.LastPullAt.Format("2006-01-02T15:04:05Z")
		}
		out = append(out, sum)
	}
	writeJSON(w, 200, out)
}

type addSubscriptionReq struct {
	Slug             string `json:"slug"`
	URL              string `json:"url"`
	Branch           string `json:"branch"`
	AuthMode         string `json:"auth_mode"` // "public" | "ssh_deploy_key" | "token"
	Token            string `json:"token,omitempty"`
	TargetCategory   string `json:"target_category,omitempty"`    // optional folder filter (e.g. "movies")
	TargetInstanceID int    `json:"target_instance_id,omitempty"` // Qui instance to pre-bind for Plan/Apply (0 = unset)
}

func (s *Server) handleAddSubscription(w http.ResponseWriter, r *http.Request) {
	var req addSubscriptionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !subSlugRegex.MatchString(req.Slug) {
		writeErr(w, 400, fmt.Errorf("invalid name %q — use lowercase letters, digits, hyphens and underscores only", req.Slug))
		return
	}
	if err := core.ValidateGitURL(req.URL); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}

	cfg := s.getConfig()
	state := s.getConsumerState()
	if state.SubscriptionSnapshot(req.Slug) != nil {
		writeErr(w, 409, fmt.Errorf("subscription %q already exists", req.Slug))
		return
	}

	auth := core.SubscriptionAuth{Mode: core.GitAuthMode(req.AuthMode)}
	switch req.AuthMode {
	case "public", "":
		auth.Mode = core.GitAuthPublic
	case "ssh_deploy_key":
		keyDir := s.gitKeyDir(req.Slug)
		auth.KeyFile = filepath.Join(keyDir, "id_ed25519")
		// Caller is expected to have created the key via POST /api/subscriptions/generate-key.
		if _, err := os.Stat(auth.KeyFile); err != nil {
			writeErr(w, 400, fmt.Errorf("ssh key missing — call /api/subscriptions/generate-key first: %w", err))
			return
		}
	case "token":
		if req.Token == "" {
			writeErr(w, 400, fmt.Errorf("token required for auth_mode=token"))
			return
		}
		keyDir := s.gitKeyDir(req.Slug)
		if err := os.MkdirAll(keyDir, 0o700); err != nil {
			writeErr(w, 500, err)
			return
		}
		auth.TokenFile = filepath.Join(keyDir, "token")
		if err := os.WriteFile(auth.TokenFile, []byte(req.Token+"\n"), 0o600); err != nil {
			writeErr(w, 500, err)
			return
		}
	default:
		writeErr(w, 400, fmt.Errorf("invalid auth_mode %q", req.AuthMode))
		return
	}

	if err := state.AddSubscription(req.Slug, req.URL, req.Branch, auth, strings.TrimSpace(req.TargetCategory), req.TargetInstanceID); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]any{"slug": req.Slug})
}

// probeReq carries enough auth info to perform a one-shot lightweight
// clone for category discovery. Public is plain HTTPS. Token is the
// PAT inline. SSH probe needs the keypair already on disk under
// /config/git-keys/<slug>/ — Phase-1 of the SSH add flow generates
// it, so the user is expected to have done that first.
type probeReq struct {
	Slug     string `json:"slug,omitempty"`     // required for ssh_deploy_key (key file lookup)
	URL      string `json:"url"`
	Branch   string `json:"branch,omitempty"`
	AuthMode string `json:"auth_mode,omitempty"`
	Token    string `json:"token,omitempty"`
}

type probeCategory struct {
	Name      string `json:"name"`
	RuleCount int    `json:"rule_count"`
}

type probeResp struct {
	Categories []probeCategory `json:"categories"`
	Total      int             `json:"total"`
}

// handleProbeSubscription does a temporary shallow clone, walks the
// top-level directories, and reports each as a category with its
// rule-file count. Used by the Add/Edit modal to populate a "what
// you can subscribe to" picker so the user picks from real options
// rather than guessing folder names.
//
// The clone is into a tmp dir we delete on return — no leftover
// state. 30-second timeout so a misconfigured URL or unreachable
// host doesn't tie up the request goroutine.
func (s *Server) handleProbeSubscription(w http.ResponseWriter, r *http.Request) {
	var req probeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	url := strings.TrimSpace(req.URL)
	if err := core.ValidateGitURL(url); err != nil {
		writeErr(w, 400, err)
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		branch = "main"
	}

	auth := core.SubscriptionAuth{Mode: core.GitAuthMode(req.AuthMode)}
	switch req.AuthMode {
	case "public", "":
		auth.Mode = core.GitAuthPublic
	case "token":
		token := strings.TrimSpace(req.Token)
		if token == "" {
			// Edit-mode probe: re-typing a stored PAT just to discover
			// categories is friction the user shouldn't pay. Fall back
			// to the existing token file when slug + file both exist.
			// The slug regex check is the same one Add uses, so a
			// caller that doesn't supply a valid slug gets the clear
			// "token required" error rather than a confusing 404.
			if !subSlugRegex.MatchString(req.Slug) {
				writeErr(w, 400, fmt.Errorf("token required for token auth"))
				return
			}
			existing := filepath.Join(s.gitKeyDir(req.Slug), "token")
			if _, err := os.Stat(existing); err != nil {
				writeErr(w, 400, fmt.Errorf("token required for token auth"))
				return
			}
			auth.TokenFile = existing
		} else {
			// Fresh token from the user — hold inline via tmp file so
			// we don't write to a slug that may not be saved yet
			// (the Add flow probes BEFORE saving).
			tmpTokenDir, err := os.MkdirTemp("", "qui-sync-probe-tok-")
			if err != nil {
				writeErr(w, 500, err)
				return
			}
			defer os.RemoveAll(tmpTokenDir)
			auth.TokenFile = filepath.Join(tmpTokenDir, "token")
			if err := os.WriteFile(auth.TokenFile, []byte(token+"\n"), 0o600); err != nil {
				writeErr(w, 500, err)
				return
			}
		}
	case "ssh_deploy_key":
		if !subSlugRegex.MatchString(req.Slug) {
			writeErr(w, 400, fmt.Errorf("slug required for ssh probe — generate the deploy key first"))
			return
		}
		auth.KeyFile = filepath.Join(s.gitKeyDir(req.Slug), "id_ed25519")
		if _, err := os.Stat(auth.KeyFile); err != nil {
			writeErr(w, 400, fmt.Errorf("ssh key missing — generate it first via /api/subscriptions/generate-key"))
			return
		}
	default:
		writeErr(w, 400, fmt.Errorf("invalid auth_mode %q", req.AuthMode))
		return
	}

	tmpDir, err := os.MkdirTemp("", "qui-sync-probe-")
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Convert SubscriptionAuth (file-pointers) to GitAuth (inline secrets)
	// the same way core/sync.go does at fetch time. Token mode reads the
	// tmp file we just wrote a few lines up.
	gitAuth := core.GitAuth{Mode: auth.Mode, KeyFile: auth.KeyFile}
	if auth.Mode == core.GitAuthToken && auth.TokenFile != "" {
		tok, err := os.ReadFile(auth.TokenFile)
		if err != nil {
			writeErr(w, 500, fmt.Errorf("read token file: %w", err))
			return
		}
		gitAuth.Token = strings.TrimSpace(string(tok))
	}

	cloneDir := filepath.Join(tmpDir, "clone")
	if err := core.CloneSubscription(ctx, url, branch, cloneDir, gitAuth); err != nil {
		writeErr(w, 502, fmt.Errorf("clone failed: %w", err))
		return
	}

	cats, total := scanRepoCategories(cloneDir)
	writeJSON(w, 200, probeResp{Categories: cats, Total: total})
}

// scanRepoCategories walks the top level of a freshly-cloned repo and
// returns each non-hidden, non-archive directory that contains at
// least one .json rule file. Rule counts are by file extension only —
// matches what the Plan endpoint considers when building decisions.
func scanRepoCategories(repoDir string) ([]probeCategory, int) {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, 0
	}
	var out []probeCategory
	total := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || e.Name() == "archive" {
			continue
		}
		count := 0
		files, err := os.ReadDir(filepath.Join(repoDir, e.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
				count++
			}
		}
		if count > 0 {
			out = append(out, probeCategory{Name: e.Name(), RuleCount: count})
			total += count
		}
	}
	return out, total
}

// handleUpdateSubscription edits an existing subscription's mutable fields.
// Slug is immutable. URL change invalidates the on-disk clone (next pull
// re-clones from the new URL). Auth changes follow the same rules as add:
// token writes a new file, ssh_deploy_key requires the key to already exist.
// Empty token in token mode means "keep the existing PAT" — common case
// for editing the category without re-entering credentials.
func (s *Server) handleUpdateSubscription(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var req addSubscriptionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := core.ValidateGitURL(req.URL); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}

	cfg := s.getConfig()
	state := s.getConsumerState()
	existing := state.SubscriptionSnapshot(slug)
	if existing == nil {
		writeErr(w, 404, fmt.Errorf("subscription %q not found", slug))
		return
	}

	auth := core.SubscriptionAuth{Mode: core.GitAuthMode(req.AuthMode)}
	switch req.AuthMode {
	case "public", "":
		auth.Mode = core.GitAuthPublic
	case "ssh_deploy_key":
		keyDir := s.gitKeyDir(slug)
		auth.KeyFile = filepath.Join(keyDir, "id_ed25519")
		if _, err := os.Stat(auth.KeyFile); err != nil {
			writeErr(w, 400, fmt.Errorf("ssh key missing — call /api/subscriptions/generate-key first: %w", err))
			return
		}
	case "token":
		keyDir := s.gitKeyDir(slug)
		auth.TokenFile = filepath.Join(keyDir, "token")
		if req.Token != "" {
			if err := os.MkdirAll(keyDir, 0o700); err != nil {
				writeErr(w, 500, err)
				return
			}
			if err := os.WriteFile(auth.TokenFile, []byte(req.Token+"\n"), 0o600); err != nil {
				writeErr(w, 500, err)
				return
			}
		} else if _, err := os.Stat(auth.TokenFile); err != nil {
			writeErr(w, 400, fmt.Errorf("no existing token to keep — provide a token"))
			return
		}
	default:
		writeErr(w, 400, fmt.Errorf("invalid auth_mode %q", req.AuthMode))
		return
	}

	if existing.URL != req.URL {
		_ = os.RemoveAll(core.SubscriptionCloneDir(cfg.Paths().Sources, slug))
	}

	if err := state.UpdateSubscription(slug, req.URL, req.Branch, auth, strings.TrimSpace(req.TargetCategory), req.TargetInstanceID); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"slug": slug})
}

func (s *Server) handleRemoveSubscription(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	cfg := s.getConfig()
	state := s.getConsumerState()
	if !state.RemoveSubscription(slug) {
		writeErr(w, 404, fmt.Errorf("subscription %q not found", slug))
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Best-effort cleanup of clone + auth files. Failures here don't
	// block the removal — user can rm manually if needed.
	_ = os.RemoveAll(core.SubscriptionCloneDir(cfg.Paths().Sources, slug))
	_ = os.RemoveAll(s.gitKeyDir(slug))

	writeJSON(w, 200, map[string]any{"removed": slug})
}

// ---- pull / plan / apply ----

func (s *Server) handlePullSubscription(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	cfg := s.getConfig()
	state := s.getConsumerState()
	sha, err := core.PullSubscription(r.Context(), cfg, state, slug)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"slug": slug, "sha": sha})
}

type planReq struct {
	QuiInstanceID int `json:"qui_instance_id"`
}

func (s *Server) handlePlanSubscription(w http.ResponseWriter, r *http.Request) {
	var req planReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.QuiInstanceID == 0 {
		writeErr(w, 400, fmt.Errorf("qui_instance_id is required"))
		return
	}
	slug := r.PathValue("slug")
	cfg := s.getConfig()
	state := s.getConsumerState()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	plan, err := core.PlanSync(r.Context(), cfg, client, state, slug, req.QuiInstanceID)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// Persist the target instance so auto-sync knows where to apply.
	// __local__ is the symlinked maintainer-side repo used by the
	// Publish "Apply local rules to my own Qui" flow — auto-sync skips
	// it explicitly so we mustn't even record a target for that slug
	// (a stale target_instance_id+0 from a previous build would still
	// be safe because autosync.go skips by slug, but keeping the state
	// clean avoids future readers wondering why the synthetic
	// subscription has a target).
	if slug != "__local__" {
		state.SetTargetInstanceID(slug, req.QuiInstanceID)
		_ = state.Save(cfg.Paths().State)
	}
	writeJSON(w, 200, plan)
}

func (s *Server) handleApplySubscription(w http.ResponseWriter, r *http.Request) {
	var req core.SyncApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.QuiInstanceID == 0 {
		writeErr(w, 400, fmt.Errorf("qui_instance_id is required"))
		return
	}
	slug := r.PathValue("slug")
	cfg := s.getConfig()
	state := s.getConsumerState()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	result, err := core.ApplySync(r.Context(), cfg, client, state, slug, req)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, result)
}

// ---- auth helpers ----

type generateKeyReq struct {
	Slug string `json:"slug"`
}

// handleGenerateKey creates an ed25519 keypair under /config/git-keys/<slug>/
// and returns the public key so the UI can show it to the user for
// pasting into github.com/<repo>/settings/keys.
// Private key is NEVER returned — it's 0600 on disk and read at fetch time.
func (s *Server) handleGenerateKey(w http.ResponseWriter, r *http.Request) {
	var req generateKeyReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !subSlugRegex.MatchString(req.Slug) {
		writeErr(w, 400, fmt.Errorf("invalid slug"))
		return
	}

	keyDir := s.gitKeyDir(req.Slug)
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		writeErr(w, 500, err)
		return
	}
	privPath := filepath.Join(keyDir, "id_ed25519")
	if _, err := os.Stat(privPath); err == nil {
		writeErr(w, 409, fmt.Errorf("key already exists for slug %q — delete the subscription first to regenerate", req.Slug))
		return
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	// OpenSSH PEM for private key (works with `ssh -i`).
	pkBytes, err := ssh.MarshalPrivateKey(priv, "qui-sync "+req.Slug)
	if err != nil {
		writeErr(w, 500, fmt.Errorf("marshal private key: %w", err))
		return
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(pkBytes), 0o600); err != nil {
		writeErr(w, 500, err)
		return
	}

	// Public key in OpenSSH format — what GitHub asks for.
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	pubAuthorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) +
		" qui-sync-" + req.Slug
	if err := os.WriteFile(privPath+".pub", []byte(pubAuthorized+"\n"), 0o644); err != nil {
		writeErr(w, 500, err)
		return
	}

	writeJSON(w, 201, map[string]any{
		"slug":     req.Slug,
		"key_file": privPath,
		"public":   pubAuthorized,
	})
}

// ---- auto-sync per-rule toggle ----

type setAutoSyncReq struct {
	RepoPath string `json:"repo_path"`
	AutoSync bool   `json:"auto_sync"`
}

func (s *Server) handleSetRuleAutoSync(w http.ResponseWriter, r *http.Request) {
	var req setAutoSyncReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	slug := r.PathValue("slug")
	cfg := s.getConfig()
	state := s.getConsumerState()
	if ok := state.SetRuleAutoSync(slug, req.RepoPath, req.AutoSync); !ok {
		writeErr(w, 404, fmt.Errorf("rule %q not found in subscription %q — run Apply first", req.RepoPath, slug))
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"repo_path": req.RepoPath, "auto_sync": req.AutoSync})
}

type setRuleDecisionReq struct {
	RepoPath      string `json:"repo_path"`
	QuiInstanceID int    `json:"qui_instance_id"`
	Action        string `json:"action"`         // skip | create_new | update_existing
	QuiRuleID     int    `json:"qui_rule_id,omitempty"` // required for update_existing
}

// handleSetRuleDecision persists a single rule's decision (skip / link /
// create_new) immediately, without waiting for Apply. Lets the user
// build up an exclude-list (skip decisions) across Plan refreshes
// without losing their choices on every pull. Symmetric for un-skip:
// the Include-again button on the "Excluded by you" Plan section calls
// this with action=create_new to drop the rule back into the main
// table; the next Plan refresh auto-matches by name as usual.
//
// Unlike SetRuleAutoSync, this creates the state entry if missing —
// so a brand-new rule can be excluded before it has ever been Applied.
func (s *Server) handleSetRuleDecision(w http.ResponseWriter, r *http.Request) {
	var req setRuleDecisionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.RepoPath == "" {
		writeErr(w, 400, fmt.Errorf("repo_path is required"))
		return
	}
	slug := r.PathValue("slug")
	cfg := s.getConfig()
	state := s.getConsumerState()
	// Default qui_instance_id from the subscription's pre-bound target
	// when the client didn't send one — saves the UI from having to
	// know about the binding. 404 when the slug doesn't exist so the
	// caller doesn't accidentally write an orphan state entry that the
	// next Plan run silently drops (entry.QuiInstanceID==0 fails the
	// instance match in PlanSync). Same shape as handleSetRuleAutoSync.
	snap := state.SubscriptionSnapshot(slug)
	if snap == nil {
		writeErr(w, 404, fmt.Errorf("subscription %q not found", slug))
		return
	}
	instanceID := req.QuiInstanceID
	if instanceID == 0 {
		instanceID = snap.TargetInstanceID
	}
	if err := state.SetRuleDecision(slug, req.RepoPath, instanceID, req.QuiRuleID, req.Action, nil); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"repo_path":   req.RepoPath,
		"action":      req.Action,
		"qui_rule_id": req.QuiRuleID,
	})
}

// ---- auto-pull interval ----

type autoPullIntervalReq struct {
	Interval string `json:"interval"` // off, 5m, 1h, 6h, 12h, 24h
}

func (s *Server) handleSetAutoPullInterval(w http.ResponseWriter, r *http.Request) {
	var req autoPullIntervalReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	valid := map[string]bool{"off": true, "5m": true, "1h": true, "6h": true, "12h": true, "24h": true}
	if !valid[req.Interval] {
		writeErr(w, 400, fmt.Errorf("invalid interval %q — use off, 5m, 1h, 6h, 12h, or 24h", req.Interval))
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.AutoPullInterval = req.Interval
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	writeJSON(w, 200, map[string]string{"interval": req.Interval})
}

// ---- push token ----

type pushTokenReq struct {
	Token string `json:"token"`
}

// validatePushToken calls GitHub's /user endpoint with the given PAT so a
// bad token is caught in Settings rather than surfacing as a cryptic TTY
// error at push time. Returns the authenticated login on success so the
// UI can show "Validated as <user>". The caller's context propagates all
// the way through so a disconnected browser cancels the outbound call.
func validatePushToken(ctx context.Context, token string) (login string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	// SSRF-safe client — api.github.com resolves to public IPs, but the
	// safe client is cheap insurance against a poisoned DNS that might
	// redirect to a private range. Same posture as clonarr's outbound
	// integrations.
	client := netsec.NewSafeHTTPClient(10*time.Second, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "", fmt.Errorf("GitHub rejected the token (401 Unauthorized) — it may be revoked, expired, or mistyped")
	}
	if resp.StatusCode == 403 {
		return "", fmt.Errorf("GitHub denied the request (403) — if you use a fine-grained PAT, check that it has access to the target repository")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}

	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if body.Login == "" {
		return "", fmt.Errorf("GitHub did not return a login name")
	}
	return body.Login, nil
}

func (s *Server) handleSavePushToken(w http.ResponseWriter, r *http.Request) {
	var req pushTokenReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeErr(w, 400, fmt.Errorf("token is required"))
		return
	}
	login, err := validatePushToken(r.Context(), token)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "saved", "login": login})
}

// handleValidatePushToken re-tests the stored token on demand. Used by the
// "Validate" button so the user can confirm a previously-saved token is
// still accepted by GitHub without re-typing it.
func (s *Server) handleValidatePushToken(w http.ResponseWriter, r *http.Request) {
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("no token stored yet"))
		return
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		writeErr(w, 400, fmt.Errorf("stored token is empty"))
		return
	}
	login, err := validatePushToken(r.Context(), token)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "valid", "login": login})
}

// ---- git remote setup ----

type gitRemoteReq struct {
	URL         string `json:"url"`
	DisplayName string `json:"display_name,omitempty"`
}

func (s *Server) handleSetGitRemote(w http.ResponseWriter, r *http.Request) {
	var req gitRemoteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := core.ValidateGitURL(req.URL); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	if err := core.SetupGitRepo(r.Context(), cfg.Paths().Repo, req.URL); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Persist display name in config.yml — survives a remote URL
	// change and a disconnect/reconnect cycle. Trim whitespace so a
	// stray space doesn't sneak into the YAML and look odd on the next
	// load. Empty string clears the name (UI falls back to URL-derived).
	newCfg := *cfg
	newCfg.RepoDisplayName = strings.TrimSpace(req.DisplayName)
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"status":       "ok",
		"url":          req.URL,
		"display_name": newCfg.RepoDisplayName,
	})
}

// handleDeleteGitRemote disconnects the local share-repo from GitHub:
// removes the origin remote, deletes the saved push token. The local
// working tree, git history, and any exported rule files stay on
// disk — only the "where to publish" link is severed. User can
// reconnect by setting URL + token again.
func (s *Server) handleDeleteGitRemote(w http.ResponseWriter, r *http.Request) {
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	if err := core.RemoveGitRemote(r.Context(), cfg.Paths().Repo); err != nil {
		writeErr(w, 500, err)
		return
	}
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		writeErr(w, 500, fmt.Errorf("delete token file: %w", err))
		return
	}
	// Clear the display name too — re-adding starts fresh. Surface any
	// SaveConfig failure: the remote is already disconnected and the
	// token deleted, so silently keeping the in-memory display name
	// would leave the UI showing a label for a repo that's no longer
	// configured. 500 lets the user retry once the underlying problem
	// (disk full, permissions) is fixed.
	if cfg.RepoDisplayName != "" {
		newCfg := *cfg
		newCfg.RepoDisplayName = ""
		if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
			writeErr(w, 500, fmt.Errorf("clear display name: %w", err))
			return
		}
		s.mu.Lock()
		s.cfg = &newCfg
		s.mu.Unlock()
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleGetGitRemote(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	url, err := core.GetGitRemote(r.Context(), cfg.Paths().Repo)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"configured":   false,
			"url":          "",
			"display_name": cfg.RepoDisplayName,
		})
		return
	}
	writeJSON(w, 200, map[string]any{
		"configured":   true,
		"url":          url,
		"display_name": cfg.RepoDisplayName,
	})
}

// ---- helper for the UI's link-to-existing dropdown ----

type quiRuleBrief struct {
	QuiRuleID int    `json:"qui_rule_id"`
	Name      string `json:"name"`
}

func (s *Server) handleListQuiRules(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	instanceID, err := strconv.Atoi(idStr)
	if err != nil || instanceID <= 0 {
		writeErr(w, 400, fmt.Errorf("invalid instance id %q", idStr))
		return
	}
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	rules, err := client.ListAutomations(r.Context(), instanceID)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	out := make([]quiRuleBrief, 0, len(rules))
	for _, r := range rules {
		out = append(out, quiRuleBrief{QuiRuleID: r.ID, Name: r.Name})
	}
	writeJSON(w, 200, out)
}
