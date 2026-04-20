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

	"github.com/prophetse7en/qui-sync/core"
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
	RepoCategories   []string `json:"repo_categories,omitempty"` // top-level dirs in the clone (for the edit-modal dropdown)
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
			Slug:           slug,
			URL:            sub.URL,
			Branch:         sub.Branch,
			AuthMode:       string(sub.Auth.Mode),
			TargetCategory: sub.TargetCategory,
			LastPullSHA:    sub.LastPullSHA,
			RuleCount:      len(sub.Rules),
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
	Slug           string `json:"slug"`
	URL            string `json:"url"`
	Branch         string `json:"branch"`
	AuthMode       string `json:"auth_mode"` // "public" | "ssh_deploy_key" | "token"
	Token          string `json:"token,omitempty"`
	TargetCategory string `json:"target_category,omitempty"` // optional folder filter (e.g. "movies")
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
	if req.URL == "" {
		writeErr(w, 400, fmt.Errorf("url is required"))
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

	if err := state.AddSubscription(req.Slug, req.URL, req.Branch, auth, strings.TrimSpace(req.TargetCategory)); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]any{"slug": req.Slug})
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
	if req.URL == "" {
		writeErr(w, 400, fmt.Errorf("url is required"))
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

	if err := state.UpdateSubscription(slug, req.URL, req.Branch, auth, strings.TrimSpace(req.TargetCategory)); err != nil {
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
	sub := state.Subscription(slug)
	sub.TargetInstanceID = req.QuiInstanceID
	_ = state.Save(cfg.Paths().State)
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
	sub := state.Subscription(slug)
	rule := sub.Rules[req.RepoPath]
	if rule == nil {
		writeErr(w, 404, fmt.Errorf("rule %q not found in subscription %q — run Apply first", req.RepoPath, slug))
		return
	}
	rule.AutoSync = req.AutoSync
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"repo_path": req.RepoPath, "auto_sync": req.AutoSync})
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

	client := &http.Client{Timeout: 10 * time.Second}
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
	URL string `json:"url"`
}

func (s *Server) handleSetGitRemote(w http.ResponseWriter, r *http.Request) {
	var req gitRemoteReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.URL == "" {
		writeErr(w, 400, fmt.Errorf("url is required"))
		return
	}
	cfg := s.getConfig()
	if err := core.SetupGitRepo(r.Context(), cfg.Paths().Repo, req.URL); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "url": req.URL})
}

func (s *Server) handleGetGitRemote(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	url, err := core.GetGitRemote(r.Context(), cfg.Paths().Repo)
	if err != nil {
		writeJSON(w, 200, map[string]any{"configured": false, "url": ""})
		return
	}
	writeJSON(w, 200, map[string]any{"configured": true, "url": url})
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
