// Package api exposes the core/ logic over HTTP for the qui-sync web UI.
//
// Design notes:
//   - All handlers return JSON (success or {"error":"..."} with a non-2xx status).
//   - The core package is called with request context so Ctrl-C, graceful
//     shutdown, and client disconnects propagate.
//   - The API surface is intentionally narrow in v0.1 — just what the Export
//     tab needs. Settings writes and Sync (consumer mode) come later.
package api

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/prophetse7en/qui-sync/internal/core"
)

// Server wires HTTP handlers to a loaded config. Config can be reloaded at
// runtime via ReloadConfig — the atomic pointer swap keeps in-flight requests
// on the old config consistent.
//
// ConsumerState is owned by the Server and shared across all sync handlers.
// Loading per-request would cause lost writes on concurrent mutations since
// each load returns a fresh object with its own mutex. The Server keeps one
// live pointer; handlers mutate it under its internal sync.RWMutex.
//
// BackupScheduler is injected post-construction (main.go calls
// SetBackupScheduler) so the backup-config handler can call Reload()
// after a Save without wiring a circular dependency between api and
// core. nil is tolerated: handlers no-op the reload when the scheduler
// hasn't been wired (e.g. in unit tests).
type Server struct {
	mu sync.RWMutex
	// cfgWriteMu serializes load → modify → SaveConfig → swap so two
	// concurrent UI saves can't lose each other's changes. Held by every
	// config-mutating handler from entry through the s.cfg pointer swap.
	// Distinct from s.mu so concurrent reads (getConfig, getConsumerState)
	// stay non-blocking while a save is in flight.
	cfgWriteMu      sync.Mutex
	cfg             *core.Config
	consumerState   *core.ConsumerState
	cfgPath         string
	staticFS        fs.FS
	startedAt       time.Time
	version         string
	backupScheduler *core.BackupScheduler
	backupRuns      *core.BackupRunStore
}

func NewServer(cfgPath string, cfg *core.Config, staticFS fs.FS, version string) (*Server, error) {
	consumerState, err := core.LoadConsumerState(cfg.Paths().State)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:           cfg,
		consumerState: consumerState,
		cfgPath:       cfgPath,
		staticFS:      staticFS,
		startedAt:     time.Now(),
		version:       version,
	}, nil
}

func (s *Server) getConfig() *core.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// ReloadConfig re-reads the config file from disk and swaps the pointer.
func (s *Server) ReloadConfig() error {
	cfg, err := core.LoadConfig(s.cfgPath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// GetConfig returns the current config. Exported for auto-sync worker.
func (s *Server) GetConfig() *core.Config { return s.getConfig() }

// GetConsumerState returns the shared state. Exported for auto-sync worker.
func (s *Server) GetConsumerState() *core.ConsumerState { return s.getConsumerState() }

// NewClient creates a QuiClient from current config. Exported for auto-sync worker.
func (s *Server) NewClient() (*core.QuiClient, error) { return s.newClient() }

// SetBackupScheduler injects the running scheduler so config-save
// handlers can Reload() it. Wired by main.go right after
// NewBackupScheduler. Idempotent.
func (s *Server) SetBackupScheduler(b *core.BackupScheduler) {
	s.mu.Lock()
	s.backupScheduler = b
	s.mu.Unlock()
}

// SetBackupRunStore injects the run-store so list/delete/run handlers
// can read and update last-run timestamps. Wired by main.go after
// loading the store. Idempotent.
func (s *Server) SetBackupRunStore(rs *core.BackupRunStore) {
	s.mu.Lock()
	s.backupRuns = rs
	s.mu.Unlock()
}

// BackupRuns returns the run-store handle for handlers that need to
// read/write last-run. nil-safe — handlers should fall back gracefully.
func (s *Server) BackupRuns() *core.BackupRunStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backupRuns
}

// reloadBackupScheduler is the handler-side helper. nil-safe so unit
// tests that don't wire a scheduler don't blow up.
func (s *Server) reloadBackupScheduler() {
	s.mu.RLock()
	b := s.backupScheduler
	s.mu.RUnlock()
	if b != nil {
		b.Reload()
	}
}

// getConsumerState returns the Server-owned consumer state pointer.
// Callers mutate through ConsumerState's own internal RWMutex; the
// Server's outer mutex only protects the pointer itself.
func (s *Server) getConsumerState() *core.ConsumerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.consumerState
}

// RegisterRoutes wires every API + static handler onto the given mux.
// Call once at startup. main.go owns the mux so it can also register
// auth routes via api.InitAuth and apply auth/CSRF middleware on top.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// API.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config/reload", s.handleReloadConfig)
	mux.HandleFunc("GET /api/instances", s.handleListInstances)
	mux.HandleFunc("GET /api/instances/{id}/rules", s.handleInstanceRules)
	mux.HandleFunc("GET /api/rules", s.handleListRules)
	mux.HandleFunc("POST /api/export/preview", s.handleExportPreview)
	mux.HandleFunc("POST /api/export/run", s.handleExportRun)
	mux.HandleFunc("GET /api/export/last-note", s.handleGetLastExportNote)
	mux.HandleFunc("PUT /api/export/last-note", s.handleUpdateLastExportNote)
	mux.HandleFunc("GET /api/changelog", s.handleChangelog)
	mux.HandleFunc("GET /api/repo/status", s.handleRepoStatus)
	mux.HandleFunc("POST /api/repo/push", s.handleRepoPush)
	mux.HandleFunc("POST /api/repo/pull", s.handleRepoPull)
	mux.HandleFunc("POST /api/repo/apply-to-qui", s.handleApplyToQui)
	mux.HandleFunc("PUT /api/excludes", s.handleSetExclude)
	mux.HandleFunc("PUT /api/excludes/instance", s.handleSetInstanceExclude)
	mux.HandleFunc("PUT /api/instances/{id}/category", s.handleRenameCategory)
	mux.HandleFunc("PUT /api/sort-order", s.handleSetSortOrder)

	// Sync (consumer-mode) — v0.2 MVP.
	mux.HandleFunc("GET /api/subscriptions", s.handleListSubscriptions)
	mux.HandleFunc("POST /api/subscriptions", s.handleAddSubscription)
	mux.HandleFunc("PUT /api/subscriptions/{slug}", s.handleUpdateSubscription)
	mux.HandleFunc("DELETE /api/subscriptions/{slug}", s.handleRemoveSubscription)
	mux.HandleFunc("POST /api/subscriptions/{slug}/pull", s.handlePullSubscription)
	mux.HandleFunc("POST /api/subscriptions/{slug}/plan", s.handlePlanSubscription)
	mux.HandleFunc("POST /api/subscriptions/{slug}/apply", s.handleApplySubscription)
	mux.HandleFunc("POST /api/subscriptions/generate-key", s.handleGenerateKey)
	mux.HandleFunc("POST /api/subscriptions/probe", s.handleProbeSubscription)
	mux.HandleFunc("GET /api/instances/{id}/qui-rules", s.handleListQuiRules)

	// Backup & Restore.
	mux.HandleFunc("POST /api/backup/{id}", s.handleCreateBackup)
	mux.HandleFunc("GET /api/backups", s.handleListBackups)
	mux.HandleFunc("POST /api/backup/restore", s.handleRestoreBackup)
	mux.HandleFunc("PUT /api/subscriptions/{slug}/rules/auto-sync", s.handleSetRuleAutoSync)
	mux.HandleFunc("PUT /api/config/auto-pull-interval", s.handleSetAutoPullInterval)
	mux.HandleFunc("PUT /api/config/push-token", s.handleSavePushToken)
	mux.HandleFunc("POST /api/config/push-token/validate", s.handleValidatePushToken)
	mux.HandleFunc("GET /api/repo/rule-diff", s.handleRepoRuleDiff)
	mux.HandleFunc("POST /api/repo/reset-to-remote", s.handleResetLocalToRemote)
	mux.HandleFunc("PUT /api/config/git-remote", s.handleSetGitRemote)
	mux.HandleFunc("GET /api/config/git-remote", s.handleGetGitRemote)
	mux.HandleFunc("DELETE /api/config/git-remote", s.handleDeleteGitRemote)
	mux.HandleFunc("PUT /api/config/qui", s.handleUpdateQuiConfig)

	// Backup schedules — full CRUD (replaces the v0.2 single PUT
	// /api/config/backup). Each schedule is named, has its own cron,
	// instance set, and retention.
	mux.HandleFunc("GET /api/backup/schedules", s.handleListSchedules)
	mux.HandleFunc("POST /api/backup/schedules", s.handleCreateSchedule)
	mux.HandleFunc("PUT /api/backup/schedules/{id}", s.handleUpdateSchedule)
	mux.HandleFunc("DELETE /api/backup/schedules/{id}", s.handleDeleteSchedule)
	mux.HandleFunc("POST /api/backup/schedules/{id}/run", s.handleRunSchedule)
	mux.HandleFunc("PUT /api/backup/gitignored", s.handleSetBackupGitignored)
	mux.HandleFunc("GET /api/qui-instances", s.handleListQuiInstances)
	mux.HandleFunc("POST /api/instances", s.handleAddInstance)
	mux.HandleFunc("DELETE /api/instances/{id}", s.handleRemoveInstance)

	// Static UI. http.FileServer maps "/" to index.html, "/css/styles.css"
	// to ui/static/css/styles.css, etc. — auth middleware allowlists the
	// /css/, /js/, /icons/ prefixes so they serve pre-login.
	mux.Handle("GET /", http.FileServer(http.FS(s.staticFS)))
}

// AccessLog wraps a handler with one-line-per-request logging. main.go
// applies it as the innermost wrapper so auth/CSRF/security-headers
// surround it; that way every request — including 401 redirects — is
// logged exactly once.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s → %d (%s)", r.Method, r.URL.Path, sw.code, time.Since(start).Truncate(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

// ---- shared helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ---- top-level handlers ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status":  "ok",
		"uptime":  time.Since(s.startedAt).Truncate(time.Second).String(),
		"version": s.version,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"version": s.version})
}
