package api

import (
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
	Slug          string `json:"slug"`
	URL           string `json:"url"`
	Branch        string `json:"branch"`
	AuthMode      string `json:"auth_mode"`
	LastPullSHA   string `json:"last_pull_sha,omitempty"`
	LastPullAt    string `json:"last_pull_at,omitempty"`
	RuleCount     int    `json:"rule_count"`      // rules with decisions in state
	RepoRuleCount int    `json:"repo_rule_count"` // rules found in the cloned repo
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
			Slug:        slug,
			URL:         sub.URL,
			Branch:      sub.Branch,
			AuthMode:    string(sub.Auth.Mode),
			LastPullSHA: sub.LastPullSHA,
			RuleCount:   len(sub.Rules),
		}
		// Count rules in the cloned repo on disk (if pulled).
		cloneDir := core.SubscriptionCloneDir(cfg.Paths().Sources, slug)
		if entries, err := os.ReadDir(filepath.Join(cloneDir, "rules")); err == nil {
			for _, cat := range entries {
				if !cat.IsDir() {
					continue
				}
				if files, err := os.ReadDir(filepath.Join(cloneDir, "rules", cat.Name())); err == nil {
					for _, f := range files {
						if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
							sum.RepoRuleCount++
						}
					}
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
	Slug     string `json:"slug"`
	URL      string `json:"url"`
	Branch   string `json:"branch"`
	AuthMode string `json:"auth_mode"` // "public" | "ssh_deploy_key" | "token"
	Token    string `json:"token,omitempty"`
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

	if err := state.AddSubscription(req.Slug, req.URL, req.Branch, auth); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]any{"slug": req.Slug})
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

func (s *Server) handleSavePushToken(w http.ResponseWriter, r *http.Request) {
	var req pushTokenReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Token == "" {
		writeErr(w, 400, fmt.Errorf("token is required"))
		return
	}
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	if err := os.WriteFile(tokenPath, []byte(req.Token+"\n"), 0o600); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "saved"})
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
