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

	"github.com/prophetse7en/qui-sync/core"
)

// Server wires HTTP handlers to a loaded config. Config can be reloaded at
// runtime via ReloadConfig — the atomic pointer swap keeps in-flight requests
// on the old config consistent.
//
// ConsumerState is owned by the Server and shared across all sync handlers.
// Loading per-request would cause lost writes on concurrent mutations since
// each load returns a fresh object with its own mutex. The Server keeps one
// live pointer; handlers mutate it under its internal sync.RWMutex.
type Server struct {
	mu            sync.RWMutex
	cfg           *core.Config
	consumerState *core.ConsumerState
	cfgPath       string
	staticFS      fs.FS
	startedAt     time.Time
	version       string
	middlewares   []Middleware
}

type Middleware func(http.Handler) http.Handler

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

// Use appends a middleware. v0.1 leaves this as an extension hook (auth, etc.).
func (s *Server) Use(m Middleware) {
	s.middlewares = append(s.middlewares, m)
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

// getConsumerState returns the Server-owned consumer state pointer.
// Callers mutate through ConsumerState's own internal RWMutex; the Server's
// outer mutex only protects the pointer itself (for ReloadConsumerState).
func (s *Server) getConsumerState() *core.ConsumerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.consumerState
}

// ReloadConsumerState re-reads consumer.state.json from disk and swaps the
// pointer. Used when state was modified externally (rare in practice).
func (s *Server) ReloadConsumerState() error {
	state, err := core.LoadConsumerState(s.cfg.RepoDir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.consumerState = state
	s.mu.Unlock()
	return nil
}

// Handler builds the route table. Call once at startup.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

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
	mux.HandleFunc("GET /api/changelog", s.handleChangelog)
	mux.HandleFunc("GET /api/repo/status", s.handleRepoStatus)
	mux.HandleFunc("POST /api/repo/push", s.handleRepoPush)
	mux.HandleFunc("PUT /api/excludes", s.handleSetExclude)
	mux.HandleFunc("PUT /api/excludes/instance", s.handleSetInstanceExclude)
	mux.HandleFunc("PUT /api/instances/{id}/category", s.handleRenameCategory)
	mux.HandleFunc("PUT /api/sort-order", s.handleSetSortOrder)

	// Sync (consumer-mode) — v0.2 MVP.
	mux.HandleFunc("GET /api/subscriptions", s.handleListSubscriptions)
	mux.HandleFunc("POST /api/subscriptions", s.handleAddSubscription)
	mux.HandleFunc("DELETE /api/subscriptions/{slug}", s.handleRemoveSubscription)
	mux.HandleFunc("POST /api/subscriptions/{slug}/pull", s.handlePullSubscription)
	mux.HandleFunc("POST /api/subscriptions/{slug}/plan", s.handlePlanSubscription)
	mux.HandleFunc("POST /api/subscriptions/{slug}/apply", s.handleApplySubscription)
	mux.HandleFunc("POST /api/subscriptions/generate-key", s.handleGenerateKey)
	mux.HandleFunc("GET /api/instances/{id}/qui-rules", s.handleListQuiRules)
	mux.HandleFunc("PUT /api/subscriptions/{slug}/rules/auto-sync", s.handleSetRuleAutoSync)
	mux.HandleFunc("PUT /api/config/auto-pull-interval", s.handleSetAutoPullInterval)
	mux.HandleFunc("PUT /api/config/push-token", s.handleSavePushToken)
	mux.HandleFunc("PUT /api/config/qui", s.handleUpdateQuiConfig)
	mux.HandleFunc("PUT /api/config/backup", s.handleUpdateBackupConfig)
	mux.HandleFunc("GET /api/qui-instances", s.handleListQuiInstances)
	mux.HandleFunc("POST /api/instances", s.handleAddInstance)
	mux.HandleFunc("DELETE /api/instances/{id}", s.handleRemoveInstance)

	// Static UI.
	mux.Handle("GET /", http.FileServer(http.FS(s.staticFS)))

	// Apply middlewares in reverse so the first registered runs outermost.
	var h http.Handler = mux
	for i := len(s.middlewares) - 1; i >= 0; i-- {
		h = s.middlewares[i](h)
	}
	return accessLog(h)
}

func accessLog(next http.Handler) http.Handler {
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
