package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prophetse7en/qui-sync/internal/core"
)

// Category names: lowercase letters, digits, hyphens, underscores.
// Must start with a letter or digit. Max 63 chars. Matches conservative
// filesystem naming — cross-platform safe and plays well with git.
var categoryRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ---- config ----

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	// API key handling: present-flag only. The filename (not the path) is
	// surfaced so users can confirm they're pointing at the right file.
	keyFileName := ""
	if cfg.Qui.APIKeyFile != "" {
		keyFileName = filepath.Base(cfg.Qui.APIKeyFile)
	}
	out := map[string]any{
		"mode":     cfg.Mode,
		"repo_dir": cfg.RepoDir,
		"qui": map[string]any{
			"url":              cfg.Qui.URL,
			"api_key_present":  cfg.Qui.APIKey != "" || cfg.Qui.APIKeyFile != "",
			"api_key_source":   keySource(cfg),
			"api_key_filename": keyFileName,
		},
		"export_instances":   cfg.ExportInstances,
		"strip_fields":       cfg.StripFields,
		"backup_schedules":   cfg.BackupSchedules,
		"backup_gitignored":  cfg.BackupGitignored,
		"auto_pull_interval": cfg.AutoPullInterval,
		"repo_display_name":  cfg.RepoDisplayName,
	}
	writeJSON(w, 200, out)
}

func keySource(cfg *core.Config) string {
	switch {
	case cfg.Qui.APIKey != "":
		return "inline"
	case cfg.Qui.APIKeyFile != "":
		return "file"
	}
	return "none"
}

func (s *Server) handleReloadConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.ReloadConfig(); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "reloaded"})
}

// ---- instances ----

type instanceStatus struct {
	QuiInstanceID int `json:"qui_instance_id"`
	// QuiName comes from Qui's /api/instances (e.g. "qBit-Movies") — shown
	// as the primary identifier in the UI so users can match against what
	// they see in the Qui app itself.
	QuiName       string `json:"qui_name"`
	Category      string `json:"category"` // Folder name on disk == in the repo
	Ping          string `json:"ping"`     // ok | fail
	PingError     string `json:"ping_error,omitempty"`
	RuleCount     int    `json:"rule_count"`
	ExcludedCount int    `json:"excluded_count"`
	Excluded      bool   `json:"excluded"` // entire instance opted out
}

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	// Fetch Qui's instance list once up-front so we can label each
	// configured export instance by its Qui name. Failure is non-fatal —
	// we fall back to "Instance <id>" in the UI.
	quiNameByID := map[int]string{}
	if quiInstances, err := client.ListInstances(r.Context()); err == nil {
		for _, qi := range quiInstances {
			quiNameByID[qi.ID] = qi.Name
		}
	}

	results := make([]instanceStatus, len(cfg.ExportInstances))
	var wg sync.WaitGroup
	for i, inst := range cfg.ExportInstances {
		wg.Add(1)
		go func(i int, inst core.ExportInstance) {
			defer wg.Done()
			name := quiNameByID[inst.QuiInstanceID]
			if name == "" {
				name = fmt.Sprintf("Instance %d", inst.QuiInstanceID)
			}
			results[i] = instanceStatus{
				QuiInstanceID: inst.QuiInstanceID,
				QuiName:       name,
				Category:      inst.Category,
				Ping:          "ok",
				Excluded:      state.IsInstanceExcluded(inst.QuiInstanceID),
			}
			rules, err := client.ListAutomations(r.Context(), inst.QuiInstanceID)
			if err != nil {
				results[i].Ping = "fail"
				results[i].PingError = err.Error()
				return
			}
			results[i].RuleCount = len(rules)
			excluded := state.ExcludedSnapshot(inst.QuiInstanceID)
			for _, rule := range rules {
				if excluded[rule.ID] {
					results[i].ExcludedCount++
				}
			}
		}(i, inst)
	}
	wg.Wait()
	writeJSON(w, 200, results)
}

// ---- per-instance rules (for the expand-instance UI) ----

type ruleDetail struct {
	QuiID    int    `json:"qui_id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
	Excluded bool   `json:"excluded"`
	// QuiSortOrder is Qui's own sortOrder (read-only source of truth).
	// SortOrder is the effective value that will be written to the
	// exported file = override if set, else QuiSortOrder.
	// HasSortOverride signals that the UI should show a "reset" affordance.
	QuiSortOrder    int  `json:"qui_sort_order"`
	SortOrder       int  `json:"sort_order"`
	HasSortOverride bool `json:"has_sort_override"`
}

func (s *Server) handleInstanceRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	idStr := r.PathValue("id")
	instanceID, err := strconv.Atoi(idStr)
	if err != nil || instanceID <= 0 {
		writeErr(w, 400, fmt.Errorf("invalid instance id %q", idStr))
		return
	}
	var inst core.ExportInstance
	found := false
	for _, in := range cfg.ExportInstances {
		if in.QuiInstanceID == instanceID {
			inst = in
			found = true
			break
		}
	}
	if !found {
		writeErr(w, 404, fmt.Errorf("instance %d not configured", instanceID))
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
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	excluded := state.ExcludedSnapshot(instanceID)

	out := make([]ruleDetail, 0, len(rules))
	for _, rule := range rules {
		slug := ""
		if entry, ok := state.Lookup(instanceID, rule.ID); ok {
			slug = entry.Slug
		}
		effective := rule.SortOrder
		hasOverride := false
		if v, ok := state.SortOrderOverride(instanceID, rule.ID); ok {
			effective = v
			hasOverride = true
		}
		out = append(out, ruleDetail{
			QuiID:           rule.ID,
			Name:            rule.Name,
			Slug:            slug,
			Category:        inst.Category,
			Enabled:         rule.Enabled,
			Excluded:        excluded[rule.ID],
			QuiSortOrder:    rule.SortOrder,
			SortOrder:       effective,
			HasSortOverride: hasOverride,
		})
	}
	// Sort by effective sort order so the UI matches the intended export order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, 200, out)
}

// ---- exclude toggle ----

type excludeReq struct {
	InstanceID int  `json:"instance_id"`
	RuleID     int  `json:"rule_id"`
	Excluded   bool `json:"excluded"`
}

// ---- Qui connection edit ----

type quiConfigReq struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key,omitempty"` // optional — empty means "don't change"
}

func (s *Server) handleUpdateQuiConfig(w http.ResponseWriter, r *http.Request) {
	var req quiConfigReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.URL == "" {
		writeErr(w, 400, fmt.Errorf("url is required"))
		return
	}

	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.Qui.URL = req.URL

	// If the user provided a new key, write it to the configured key file
	// (or create a default one). We never put the secret in config.yml.
	if req.APIKey != "" {
		keyPath := cfg.Qui.APIKeyFile
		if keyPath == "" {
			keyPath = filepath.Join(filepath.Dir(s.cfgPath), "api-key")
			newCfg.Qui.APIKeyFile = keyPath
		}
		if err := os.WriteFile(keyPath, []byte(req.APIKey+"\n"), 0o600); err != nil {
			writeErr(w, 500, fmt.Errorf("write api key: %w", err))
			return
		}
		newCfg.Qui.APIKey = "" // ensure inline key never leaks into config
	}

	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Qui instance discovery + add/remove ----

type quiInstanceInfo struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

func (s *Server) handleListQuiInstances(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	quiInstances, err := client.ListInstances(r.Context())
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	configured := map[int]bool{}
	for _, inst := range cfg.ExportInstances {
		configured[inst.QuiInstanceID] = true
	}
	out := make([]quiInstanceInfo, 0, len(quiInstances))
	for _, qi := range quiInstances {
		out = append(out, quiInstanceInfo{
			ID:         qi.ID,
			Name:       qi.Name,
			Configured: configured[qi.ID],
		})
	}
	writeJSON(w, 200, out)
}

type addInstanceReq struct {
	QuiInstanceID int    `json:"qui_instance_id"`
	Category      string `json:"category"`
}

func (s *Server) handleAddInstance(w http.ResponseWriter, r *http.Request) {
	var req addInstanceReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.QuiInstanceID <= 0 {
		writeErr(w, 400, fmt.Errorf("qui_instance_id is required"))
		return
	}
	if !categoryRegex.MatchString(req.Category) {
		writeErr(w, 400, fmt.Errorf("invalid category %q", req.Category))
		return
	}

	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	for _, inst := range cfg.ExportInstances {
		if inst.QuiInstanceID == req.QuiInstanceID {
			writeErr(w, 409, fmt.Errorf("instance %d is already configured", req.QuiInstanceID))
			return
		}
		if inst.Category == req.Category {
			writeErr(w, 409, fmt.Errorf("category %q is already used by instance %d", req.Category, inst.QuiInstanceID))
			return
		}
	}

	newCfg := *cfg
	newCfg.ExportInstances = append(append([]core.ExportInstance(nil), cfg.ExportInstances...),
		core.ExportInstance{QuiInstanceID: req.QuiInstanceID, Category: req.Category})
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	writeJSON(w, 201, map[string]any{
		"qui_instance_id": req.QuiInstanceID,
		"category":        req.Category,
	})
}

func (s *Server) handleRemoveInstance(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeErr(w, 400, fmt.Errorf("invalid instance id %q", idStr))
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	idx := -1
	for i, inst := range cfg.ExportInstances {
		if inst.QuiInstanceID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		writeErr(w, 404, fmt.Errorf("instance %d not configured", id))
		return
	}
	newCfg := *cfg
	newCfg.ExportInstances = append(
		append([]core.ExportInstance(nil), cfg.ExportInstances[:idx]...),
		cfg.ExportInstances[idx+1:]...,
	)
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	// We leave state entries + on-disk files intact — re-adding the instance
	// resumes with the same slugs. Explicit cleanup stays a user action.
	writeJSON(w, 200, map[string]any{"removed": id})
}

// ---- rename category (folder) ----

type renameCategoryReq struct {
	Category string `json:"category"`
}

func (s *Server) handleRenameCategory(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	instanceID, err := strconv.Atoi(idStr)
	if err != nil || instanceID <= 0 {
		writeErr(w, 400, fmt.Errorf("invalid instance id %q", idStr))
		return
	}

	var req renameCategoryReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	newCat := req.Category
	if !categoryRegex.MatchString(newCat) {
		writeErr(w, 400, fmt.Errorf("invalid category %q — use lowercase letters, digits, - and _ (max 63 chars)", newCat))
		return
	}

	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()

	// Find the target instance in config and check for collisions.
	targetIdx := -1
	for i, inst := range cfg.ExportInstances {
		if inst.QuiInstanceID == instanceID {
			targetIdx = i
			continue
		}
		if inst.Category == newCat {
			writeErr(w, 409, fmt.Errorf("category %q is already used by instance %d", newCat, inst.QuiInstanceID))
			return
		}
	}
	if targetIdx == -1 {
		writeErr(w, 404, fmt.Errorf("instance %d not configured", instanceID))
		return
	}
	oldCat := cfg.ExportInstances[targetIdx].Category
	if oldCat == newCat {
		writeJSON(w, 200, map[string]any{"category": newCat, "unchanged": true})
		return
	}

	// Work on a deep-enough copy so we don't leave a partially-modified
	// config in memory if any step below fails.
	newCfg := *cfg
	newCfg.ExportInstances = append([]core.ExportInstance(nil), cfg.ExportInstances...)
	newCfg.ExportInstances[targetIdx].Category = newCat

	// 1. Move on-disk folder if it exists. We do this before mutating state
	// so a filesystem error aborts cleanly.
	oldDir := filepath.Join(cfg.Paths().Repo, "rules", oldCat)
	newDir := filepath.Join(cfg.Paths().Repo, "rules", newCat)
	if _, err := os.Stat(oldDir); err == nil {
		if _, err := os.Stat(newDir); err == nil {
			writeErr(w, 409, fmt.Errorf("folder rules/%s already exists on disk — move or merge manually", newCat))
			return
		}
		if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
			writeErr(w, 500, err)
			return
		}
		if err := os.Rename(oldDir, newDir); err != nil {
			writeErr(w, 500, fmt.Errorf("move rules/%s → rules/%s: %w", oldCat, newCat, err))
			return
		}
	}

	// 2. Update state entries — any rule in this instance whose category
	// was oldCat now points at newCat.
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	state.RenameCategory(instanceID, oldCat, newCat)
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}

	// 3. Persist new config to disk and swap it in memory.
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()

	writeJSON(w, 200, map[string]any{
		"instance_id": instanceID,
		"old":         oldCat,
		"new":         newCat,
	})
}

// ---- sort-order override ----

type sortOrderReq struct {
	InstanceID int  `json:"instance_id"`
	RuleID     int  `json:"rule_id"`
	SortOrder  *int `json:"sort_order,omitempty"` // pointer so null == clear
	Clear      bool `json:"clear,omitempty"`      // explicit clear flag
}

func (s *Server) handleSetSortOrder(w http.ResponseWriter, r *http.Request) {
	var req sortOrderReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.InstanceID == 0 || req.RuleID == 0 {
		writeErr(w, 400, fmt.Errorf("instance_id and rule_id are required"))
		return
	}
	cfg := s.getConfig()
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	clear := req.Clear || req.SortOrder == nil
	if clear {
		state.ClearSortOrderOverride(req.InstanceID, req.RuleID)
	} else {
		state.SetSortOrderOverride(req.InstanceID, req.RuleID, *req.SortOrder)
	}
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	resp := map[string]any{
		"instance_id": req.InstanceID,
		"rule_id":     req.RuleID,
		"cleared":     clear,
	}
	if !clear {
		resp["sort_order"] = *req.SortOrder
	}
	writeJSON(w, 200, resp)
}

type instanceExcludeReq struct {
	InstanceID int  `json:"instance_id"`
	Excluded   bool `json:"excluded"`
}

func (s *Server) handleSetInstanceExclude(w http.ResponseWriter, r *http.Request) {
	var req instanceExcludeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.InstanceID == 0 {
		writeErr(w, 400, fmt.Errorf("instance_id is required"))
		return
	}
	cfg := s.getConfig()
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	state.SetInstanceExcluded(req.InstanceID, req.Excluded)
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"instance_id": req.InstanceID,
		"excluded":    req.Excluded,
	})
}

func (s *Server) handleSetExclude(w http.ResponseWriter, r *http.Request) {
	var req excludeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.InstanceID == 0 || req.RuleID == 0 {
		writeErr(w, 400, fmt.Errorf("instance_id and rule_id are required"))
		return
	}
	cfg := s.getConfig()
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	state.SetExcluded(req.InstanceID, req.RuleID, req.Excluded)
	if err := state.Save(cfg.Paths().State); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"instance_id": req.InstanceID,
		"rule_id":     req.RuleID,
		"excluded":    req.Excluded,
	})
}

// ---- rules ----

type ruleSummary struct {
	QuiID    int    `json:"qui_id"`
	Instance int    `json:"instance"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
}

type rulesResponse struct {
	Rules  []ruleSummary  `json:"rules"`
	Errors map[int]string `json:"errors,omitempty"` // instanceID → error message
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	state, err := core.LoadMaintainerState(cfg.Paths().State)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	type fetchResult struct {
		inst  core.ExportInstance
		rules []core.Automation
		err   error
	}
	results := make([]fetchResult, len(cfg.ExportInstances))
	var wg sync.WaitGroup
	for i, inst := range cfg.ExportInstances {
		wg.Add(1)
		go func(i int, inst core.ExportInstance) {
			defer wg.Done()
			rs, err := client.ListAutomations(r.Context(), inst.QuiInstanceID)
			results[i] = fetchResult{inst: inst, rules: rs, err: err}
		}(i, inst)
	}
	wg.Wait()

	out := rulesResponse{Rules: []ruleSummary{}}
	for _, res := range results {
		if res.err != nil {
			if out.Errors == nil {
				out.Errors = map[int]string{}
			}
			out.Errors[res.inst.QuiInstanceID] = res.err.Error()
			continue
		}
		for _, rule := range res.rules {
			slug := ""
			if entry, ok := state.Lookup(res.inst.QuiInstanceID, rule.ID); ok {
				slug = entry.Slug
			}
			out.Rules = append(out.Rules, ruleSummary{
				QuiID:    rule.ID,
				Instance: res.inst.QuiInstanceID,
				Slug:     slug,
				Name:     rule.Name,
				Category: res.inst.Category,
				Enabled:  rule.Enabled,
			})
		}
	}
	sort.Slice(out.Rules, func(i, j int) bool {
		if out.Rules[i].Category != out.Rules[j].Category {
			return out.Rules[i].Category < out.Rules[j].Category
		}
		return out.Rules[i].Name < out.Rules[j].Name
	})
	writeJSON(w, 200, out)
}

// ---- export ----

func (s *Server) handleExportPreview(w http.ResponseWriter, r *http.Request) { s.doExport(w, r, true) }
func (s *Server) handleExportRun(w http.ResponseWriter, r *http.Request)     { s.doExport(w, r, false) }

// exportRequest is the optional body for the export endpoints. Clients
// may omit it entirely — empty body is treated as "run with defaults".
type exportRequest struct {
	// Note is a free-form markdown paragraph the maintainer can attach
	// to today's CHANGELOG entry, explaining why the rules changed.
	// Lives in UI state only — written directly into CHANGELOG.md at
	// export time. Previews ignore it (nothing is written on dry run).
	Note string `json:"note,omitempty"`
}

func (s *Server) doExport(w http.ResponseWriter, r *http.Request, dryRun bool) {
	cfg := s.getConfig()

	// Body is optional; tolerate empty / absent / invalid JSON so older
	// clients that send nothing still work.
	var req exportRequest
	if r.Body != nil && r.ContentLength != 0 {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req)
	}

	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	note := ""
	if !dryRun {
		note = strings.TrimSpace(req.Note)
	}
	diff, err := core.RunExport(r.Context(), cfg, client, dryRun, note)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// Auto-commit to git after a real (non-dry) export that produced changes.
	// This makes the export immediately available for Sync/Pull without
	// requiring the user to run git commit manually.
	gitCommitted := false
	if !dryRun && !diff.Empty() {
		if err := core.GitCommitExport(r.Context(), cfg.Paths().Repo); err != nil {
			log.Printf("git commit after export: %v (non-fatal)", err)
		} else {
			gitCommitted = true
		}
	}
	writeJSON(w, 200, map[string]any{
		"dry_run":       dryRun,
		"empty":         diff.Empty(),
		"added":         diff.Added,
		"updated":       diff.Updated,
		"renamed":       diff.Renamed,
		"removed":       diff.Removed,
		"moved":         diff.Moved,
		"git_committed": gitCommitted,
	})
}

// handleGetLastExportNote returns the note paragraph of the top
// section in CHANGELOG.md — used by the UI to pre-fill the Edit-note
// textarea.
func (s *Server) handleGetLastExportNote(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	note, err := core.CurrentExportNote(cfg.Paths().Repo)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"note": note})
}

// handleUpdateLastExportNote rewrites the note paragraph of the top
// section in CHANGELOG.md and creates a follow-up git commit. The
// user pushes manually when ready.
func (s *Server) handleUpdateLastExportNote(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := core.UpdateLatestExportNote(cfg.Paths().Repo, req.Note); err != nil {
		writeErr(w, 400, err)
		return
	}
	committed := false
	if err := core.GitCommitNoteUpdate(r.Context(), cfg.Paths().Repo); err != nil {
		log.Printf("git commit after note edit: %v (non-fatal)", err)
	} else {
		committed = true
	}
	writeJSON(w, 200, map[string]any{
		"note":          strings.TrimSpace(req.Note),
		"git_committed": committed,
	})
}

// ---- changelog ----

func (s *Server) handleChangelog(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	path := filepath.Join(cfg.Paths().Repo, "CHANGELOG.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		writeJSON(w, 200, map[string]string{"content": ""})
		return
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	info, _ := os.Stat(path)
	modTime := ""
	if info != nil {
		modTime = info.ModTime().Format(time.RFC3339)
	}
	writeJSON(w, 200, map[string]string{
		"content":  string(data),
		"modified": modTime,
	})
}

// handleResetLocalToRemote hard-resets /data/repo to match the current
// remote branch. Destructive — local commits not on remote are lost.
// Used for first-time setup where qui-sync's fresh git init produced a
// history that diverges from an existing remote; git push + pull refuse
// to reconcile "unrelated histories" and the user needs a clean way out.
func (s *Server) handleResetLocalToRemote(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()

	// Token is optional — public repos fetch anonymously. If a token
	// is stored we pass it through so private repos work too.
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	token := ""
	if tokenBytes, err := os.ReadFile(tokenPath); err == nil {
		token = strings.TrimSpace(string(tokenBytes))
	}

	if err := core.ResetLocalToRemote(r.Context(), cfg.Paths().Repo, token); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "reset"})
}

// ---- repo rule-level diff ----

// handleRepoRuleDiff returns a structured rule-by-rule comparison of the
// working tree vs origin/<current-branch>. Used by the Export tab's pre-push
// review so the user can see exactly which automation rules differ — and
// in what way — before running commit + push.
func (s *Server) handleRepoRuleDiff(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	result, err := core.ComputeRepoRuleDiff(r.Context(), cfg.Paths().Repo)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, result)
}

// ---- repo status + push ----

func (s *Server) handleRepoStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	// Token is best-effort here — anonymous fetch is fine for public repos.
	// Without a token on a private repo, ahead/behind stay at 0 (which is
	// less misleading now that the user can see push_configured=false in
	// the UI and is prompted to add a token in Settings).
	tokenBytes, tokenErr := os.ReadFile(tokenPath)
	token := ""
	if tokenErr == nil {
		token = strings.TrimSpace(string(tokenBytes))
	}
	status, err := core.GetRepoStatus(r.Context(), cfg.Paths().Repo, token)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	// Note: the old diff_summary field (a git diff --stat text block) was
	// removed when the structured rule-level diff from /api/repo/rule-diff
	// replaced it in the UI. If any future caller needs the short-form
	// summary it's still reachable via core.RepoDiffSummary directly.
	writeJSON(w, 200, map[string]any{
		"status":          status,
		"push_configured": tokenErr == nil,
	})
}

// handleApplyToQui is a convenience endpoint that creates a temporary
// subscription pointing at the local repo (/data/repo) and runs
// Plan+Apply through the normal Sync engine. This gives maintainers
// a "reverse export" flow: edit files → Apply to Qui, with the same
// 3-layer merge that preserves trackers and user-owned fields.
func (s *Server) handleApplyToQui(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	state := s.getConsumerState()
	repoDir := cfg.Paths().Repo

	// Ensure a __local__ subscription exists pointing at the repo dir.
	const localSlug = "__local__"
	if state.SubscriptionSnapshot(localSlug) == nil {
		_ = state.AddSubscription(localSlug, repoDir, "HEAD", core.SubscriptionAuth{Mode: core.GitAuthPublic}, "", 0)
		_ = state.Save(cfg.Paths().State)
	}

	// Create a symlink from the expected clone dir to the actual repo
	// so PlanSync can find the rules without a real git clone.
	sourcesDir := cfg.Paths().Sources
	linkPath := core.SubscriptionCloneDir(sourcesDir, localSlug)
	_ = os.MkdirAll(sourcesDir, 0o755)
	_ = os.RemoveAll(linkPath)
	if err := os.Symlink(repoDir, linkPath); err != nil {
		writeErr(w, 500, fmt.Errorf("symlink local repo: %w", err))
		return
	}

	writeJSON(w, 200, map[string]any{
		"status":  "ready",
		"slug":    localSlug,
		"message": "Local repo linked. Use the Sync tab to Plan and Apply against a Qui instance.",
	})
}

func (s *Server) handleRepoPull(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("push token not configured — needed for pull too (same auth)"))
		return
	}
	token := strings.TrimSpace(string(tokenBytes))
	if err := core.PullRepo(r.Context(), cfg.Paths().Repo, token); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "pulled"})
}

func (s *Server) handleRepoPush(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	tokenPath := filepath.Join(filepath.Dir(s.cfgPath), "git-push-token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		writeErr(w, 400, fmt.Errorf("push token not configured — create /config/git-push-token with your GitHub PAT, or paste it in Settings"))
		return
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		writeErr(w, 400, fmt.Errorf("git-push-token file is empty"))
		return
	}
	if err := core.PushRepo(r.Context(), cfg.Paths().Repo, token); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "pushed"})
}

// ---- helpers ----

func (s *Server) newClient() (*core.QuiClient, error) {
	cfg := s.getConfig()
	if !cfg.IsQuiConfigured() {
		return nil, fmt.Errorf("Qui is not configured yet — go to Settings and enter your Qui URL + API key")
	}
	key, err := cfg.ResolveAPIKey()
	if err != nil {
		return nil, err
	}
	return core.NewQuiClient(cfg.Qui.URL, key), nil
}
