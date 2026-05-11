package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/prophetse7en/qui-sync/internal/core"
)

// Backup-schedule CRUD. Each schedule is an independent named cron
// rule with its own instance set and retention. Replaces the v0.2
// single-schedule model. Routes:
//
//   GET    /api/backup/schedules
//   POST   /api/backup/schedules
//   PUT    /api/backup/schedules/{id}
//   DELETE /api/backup/schedules/{id}
//   POST   /api/backup/schedules/{id}/run     — fire immediately
//   PUT    /api/backup/gitignored              — global hide-from-git toggle

// scheduleSlugRe restricts auto-generated IDs to a filesystem- and
// URL-friendly subset. Slugs come from the user-supplied Name (lower-
// cased, non-alnum collapsed to '-') and are deduped server-side; the
// user never types one.
var scheduleSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// scheduleReq is the wire shape for create + update. ID is set by the
// server on create and validated as path-matching on update.
type scheduleReq struct {
	Name          string `json:"name"`
	Cron          string `json:"cron"`
	Enabled       bool   `json:"enabled"`
	Instances     []int  `json:"instances"`
	RetentionDays int    `json:"retention_days"`
	KeepLastN     int    `json:"keep_last_n"`
}

// validateScheduleReq centralises the validation rules so create and
// update share them. Returns a user-facing error or nil.
func validateScheduleReq(req scheduleReq) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(req.Name) > 80 {
		return fmt.Errorf("name must be 80 characters or fewer")
	}
	if req.RetentionDays < 0 || req.RetentionDays > 3650 {
		return fmt.Errorf("retention_days must be 0-3650")
	}
	if req.KeepLastN < 0 || req.KeepLastN > 10000 {
		return fmt.Errorf("keep_last_n must be 0-10000")
	}
	cronExpr := strings.TrimSpace(req.Cron)
	if req.Enabled {
		if _, err := core.ParseBackupCron(cronExpr); err != nil {
			return fmt.Errorf("cron is required when enabled: %w", err)
		}
	}
	for _, instID := range req.Instances {
		if instID <= 0 {
			return fmt.Errorf("invalid instance id %d", instID)
		}
	}
	return nil
}

// generateScheduleID derives a unique ID from the user's Name. Lower-
// cased + dash-separated. If the slug collides with an existing
// schedule, append a numeric suffix until unique.
func generateScheduleID(name string, existing []core.BackupSchedule) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = scheduleSlugRe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "backup"
	}
	taken := make(map[string]struct{}, len(existing))
	for _, s := range existing {
		taken[s.ID] = struct{}{}
	}
	if _, clash := taken[base]; !clash {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, clash := taken[candidate]; !clash {
			return candidate
		}
	}
	// Astronomically unlikely; fall back to a timestamp suffix.
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

// handleListSchedules returns the configured schedules in declaration
// order. Each entry includes both the next-fire time (computed from
// the cron expression) and the last-fire time (from BackupRunStore)
// so the UI can render a tabular row with consistent columns across
// all schedules without doing per-row API calls.
func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	cfg := s.getConfig()
	runs := s.BackupRuns()
	out := make([]map[string]any, 0, len(cfg.BackupSchedules))
	now := time.Now()
	for _, sched := range cfg.BackupSchedules {
		entry := map[string]any{
			"id":                 sched.ID,
			"name":               sched.Name,
			"cron":               sched.Cron,
			"enabled":            sched.Enabled,
			"instances":          sched.Instances,
			"retention_days":     sched.RetentionDays,
			"keep_last_n":        sched.KeepLastN,
			"resolved_instances": core.ResolveScheduleInstances(cfg, sched),
		}
		if sched.Enabled {
			if parsed, err := core.ParseBackupCron(sched.Cron); err == nil {
				entry["next_run_at"] = parsed.Next(now).UTC().Format(time.RFC3339)
			}
		}
		if runs != nil {
			if last := runs.LastRun(sched.ID); !last.IsZero() {
				entry["last_run_at"] = last.UTC().Format(time.RFC3339)
			}
		}
		out = append(out, entry)
	}
	writeJSON(w, 200, map[string]any{
		"schedules":  out,
		"gitignored": cfg.BackupGitignored,
	})
}

// handleCreateSchedule appends a new schedule and persists. The
// scheduler reloads at the end so a newly-enabled schedule starts
// firing on its cron without a container restart.
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := validateScheduleReq(req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.BackupSchedules = append([]core.BackupSchedule(nil), cfg.BackupSchedules...)
	id := generateScheduleID(req.Name, newCfg.BackupSchedules)
	newCfg.BackupSchedules = append(newCfg.BackupSchedules, core.BackupSchedule{
		ID:            id,
		Name:          strings.TrimSpace(req.Name),
		Cron:          strings.TrimSpace(req.Cron),
		Enabled:       req.Enabled,
		Instances:     req.Instances,
		RetentionDays: req.RetentionDays,
		KeepLastN:     req.KeepLastN,
	})
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	s.reloadBackupScheduler()
	writeJSON(w, 200, map[string]any{"id": id})
}

// handleUpdateSchedule replaces the schedule at the given path ID
// with the body's values. ID itself is immutable (it's the path slug).
func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, fmt.Errorf("id required"))
		return
	}
	var req scheduleReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := validateScheduleReq(req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.BackupSchedules = append([]core.BackupSchedule(nil), cfg.BackupSchedules...)
	found := false
	for i := range newCfg.BackupSchedules {
		if newCfg.BackupSchedules[i].ID == id {
			newCfg.BackupSchedules[i].Name = strings.TrimSpace(req.Name)
			newCfg.BackupSchedules[i].Cron = strings.TrimSpace(req.Cron)
			newCfg.BackupSchedules[i].Enabled = req.Enabled
			newCfg.BackupSchedules[i].Instances = req.Instances
			newCfg.BackupSchedules[i].RetentionDays = req.RetentionDays
			newCfg.BackupSchedules[i].KeepLastN = req.KeepLastN
			found = true
			break
		}
	}
	if !found {
		writeErr(w, 404, fmt.Errorf("schedule %q not found", id))
		return
	}
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	s.reloadBackupScheduler()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDeleteSchedule removes a schedule by ID. Existing snapshots
// on disk are NOT touched — schedules decide WHEN to back up; the
// folders themselves outlive the schedule that created them.
func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, fmt.Errorf("id required"))
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.BackupSchedules = make([]core.BackupSchedule, 0, len(cfg.BackupSchedules))
	found := false
	for _, sched := range cfg.BackupSchedules {
		if sched.ID == id {
			found = true
			continue
		}
		newCfg.BackupSchedules = append(newCfg.BackupSchedules, sched)
	}
	if !found {
		writeErr(w, 404, fmt.Errorf("schedule %q not found", id))
		return
	}
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	// Best-effort cleanup of the run record. If forget fails the worst
	// outcome is a stale entry in backup-runs.json that no UI will
	// ever surface — non-fatal.
	if runs := s.BackupRuns(); runs != nil {
		_ = runs.Forget(id)
	}
	s.reloadBackupScheduler()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleRunSchedule fires the schedule immediately, bypassing cron.
// Used by the "Run now" button. Returns 202 with a synchronous result
// summary — runs are short enough (single-digit seconds per instance)
// that the user can wait for the response.
func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, fmt.Errorf("id required"))
		return
	}
	cfg := s.getConfig()
	var sched *core.BackupSchedule
	for i := range cfg.BackupSchedules {
		if cfg.BackupSchedules[i].ID == id {
			tmp := cfg.BackupSchedules[i]
			sched = &tmp
			break
		}
	}
	if sched == nil {
		writeErr(w, 404, fmt.Errorf("schedule %q not found", id))
		return
	}
	instances := core.ResolveScheduleInstances(cfg, *sched)
	if len(instances) == 0 {
		writeErr(w, 400, fmt.Errorf("schedule has no instances to back up"))
		return
	}
	client, err := s.newClient()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// Cap a Run-now at 30 minutes — long enough for any reasonable
	// instance count, short enough to not strand a goroutine forever.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	var ok, failed int
	results := make([]map[string]any, 0, len(instances))
	for _, instID := range instances {
		dir, n, err := core.CreateBackup(ctx, cfg, client, instID)
		entry := map[string]any{"instance_id": instID}
		if err != nil {
			failed++
			entry["status"] = "error"
			entry["error"] = err.Error()
		} else {
			ok++
			entry["status"] = "ok"
			entry["rule_count"] = n
			entry["dir"] = dir
		}
		results = append(results, entry)
	}
	// Mark the schedule as having "just run" even when some instances
	// failed — the user explicitly asked for this fire, the timestamp
	// is meaningful regardless of the per-instance outcome.
	if runs := s.BackupRuns(); runs != nil {
		_ = runs.SetLastRun(sched.ID, time.Now())
	}
	writeJSON(w, 200, map[string]any{
		"ok":     ok,
		"failed": failed,
		"runs":   results,
	})
}

// handleSetBackupGitignored toggles the global "hide backups from
// GitHub" flag. The toggle is applied to the share-repo's .gitignore
// immediately so an existing repo with backups-full inside it stops
// staging future snapshots — and is reversed on toggle-off so a user
// who explicitly wants the backups committed gets them un-ignored.
//
// The .gitignore reconciliation only takes effect when backups-full
// is INSIDE the repo (typical when RepoDir is the volume root). When
// backups live outside the repo, the toggle is a no-op — that's the
// common case and there's no real "hiding" to do.
func (s *Server) handleSetBackupGitignored(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gitignored bool `json:"gitignored"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.BackupGitignored = req.Gitignored
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	if err := core.EnsureBackupGitignore(&newCfg); err != nil {
		// Config saved but .gitignore write failed — surface so the
		// user can fix permissions / disk-full rather than silently
		// leaving the repo state inconsistent.
		writeErr(w, 500, fmt.Errorf("update .gitignore: %w", err))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleSetArchiveGitignored toggles the "hide archive/ from GitHub"
// flag. archive/ always lives inside the share-repo (by design), so
// the .gitignore reconciliation always has an effect — there's no
// out-of-repo no-op edge case to worry about.
//
// The write is immediate so a quick toggle reflects in the next git
// status, but the user only sees the consumer-facing effect on the
// next Commit export or Push to remote where .gitignore actually
// lands in a commit.
func (s *Server) handleSetArchiveGitignored(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Gitignored bool `json:"gitignored"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.cfgWriteMu.Lock()
	defer s.cfgWriteMu.Unlock()
	cfg := s.getConfig()
	newCfg := *cfg
	newCfg.ArchiveGitignored = req.Gitignored
	if err := core.SaveConfig(s.cfgPath, &newCfg); err != nil {
		writeErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.cfg = &newCfg
	s.mu.Unlock()
	if err := core.EnsureArchiveGitignore(&newCfg); err != nil {
		writeErr(w, 500, fmt.Errorf("update .gitignore: %w", err))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
