// qui-sync server — HTTP + web UI on top of core/.
//
// Runs alongside the CLI (cmd/cli) — both share the same core package.
// Default port 6070. Graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prophetse7en/qui-sync/internal/api"
	"github.com/prophetse7en/qui-sync/internal/auth"
	"github.com/prophetse7en/qui-sync/internal/core"
	"github.com/prophetse7en/qui-sync/internal/utils"
	"github.com/prophetse7en/qui-sync/ui"
)

const version = "0.4.1-dev"

func main() {
	var (
		addr    = flag.String("addr", ":6070", "HTTP listen address")
		cfgPath = flag.String("config", core.DefaultConfigPath(), "Path to config.yml")
		webDir  = flag.String("web-dir", "", "Optional path to a ui/static directory (dev override of embedded FS)")
	)
	flag.Parse()

	cfg, err := core.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if !cfg.IsQuiConfigured() {
		log.Println("Qui not configured yet — open the UI and go to Settings to complete setup.")
	}
	// Reconcile .gitignore so testers upgrading from a build that
	// only persisted the toggle (no real effect) get the actual file
	// state on first boot. Best-effort — a missing repo or permissions
	// hiccup logs but doesn't fail startup.
	if err := core.EnsureBackupGitignore(cfg); err != nil {
		log.Printf("backup .gitignore reconcile (continuing): %v", err)
	}
	if err := core.EnsureArchiveGitignore(cfg); err != nil {
		log.Printf("archive .gitignore reconcile (continuing): %v", err)
	}

	staticFS, err := resolveStatic(*webDir)
	if err != nil {
		log.Fatalf("static: %v", err)
	}

	srv, err := api.NewServer(*cfgPath, cfg, staticFS, version)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Authentication. InitAuth registers /setup, /login, /logout,
	// /api/auth/status etc. onto the same mux and returns the live
	// auth store. The middleware chain below routes every request
	// through it before reaching the API or static handlers.
	authStore := api.InitAuth(ctx, srv.GetConfig, version, mux)

	// Background: reap expired sessions every 5 min so the in-memory
	// map doesn't grow unbounded under sustained login traffic.
	utils.SafeGo("session-cleanup", func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				authStore.CleanupExpiredSessions()
			}
		}
	})

	// Middleware chain — outermost first:
	//   SecurityHeaders → CSRF → Auth → access-log → routes
	// CSRF runs before Auth so the cookie is set on /login GET (which
	// is public-facing). Auth then enforces session/api-key/local-bypass
	// on every other path.
	var handler http.Handler = api.AccessLog(mux)
	handler = authStore.Middleware(handler)
	handler = authStore.CSRFMiddleware(handler)
	handler = auth.SecurityHeadersMiddleware(handler)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Auto-sync background worker.
	worker := core.NewAutoSyncWorker(srv.GetConfig, srv.GetConsumerState, srv.NewClient)
	utils.SafeGo("auto-sync-worker", func() { worker.Start(ctx) })

	// Scheduled full-instance backups. robfig/cron owns the timing —
	// callbacks fire exactly at the next configured cron slot, never
	// at restart. SetBackupScheduler wires the same instance into the
	// API server so a Settings save can call Reload() and have the new
	// cron take effect immediately.
	backupScheduler := core.NewBackupScheduler(srv.GetConfig, srv.NewClient)
	srv.SetBackupScheduler(backupScheduler)

	// BackupRunStore persists per-schedule "last run" timestamps so the
	// UI can show "last run 2h ago" without scanning every backup
	// folder. Load here so a missing file (first run, or testers
	// upgrading from a build before the store existed) is handled
	// gracefully rather than fataling.
	backupRuns := core.NewBackupRunStore(filepath.Dir(*cfgPath))
	if err := backupRuns.Load(); err != nil {
		log.Printf("backup-runs: load failed (continuing with empty store): %v", err)
	}
	backupScheduler.SetRunStore(backupRuns)
	srv.SetBackupRunStore(backupRuns)

	utils.SafeGo("backup-scheduler", func() { backupScheduler.Start(ctx) })

	errCh := make(chan error, 1)
	utils.SafeGo("http-listener", func() {
		log.Printf("qui-sync server %s — listening on %s (config: %s)", version, *addr, *cfgPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	})

	select {
	case <-ctx.Done():
		log.Println("shutting down…")
	case err := <-errCh:
		if err != nil {
			log.Printf("listen error: %v", err)
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("bye")
}

// resolveStatic returns the static filesystem. If webDir is set, reads from
// disk (for live UI editing during development). Otherwise uses the embedded
// copy baked in at build time.
func resolveStatic(webDir string) (fs.FS, error) {
	if webDir != "" {
		info, err := os.Stat(webDir)
		if err != nil {
			return nil, fmt.Errorf("web-dir: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("web-dir: %s is not a directory", webDir)
		}
		return os.DirFS(webDir), nil
	}
	sub, err := fs.Sub(ui.StaticFiles, "static")
	if err != nil {
		return nil, err
	}
	return sub, nil
}
