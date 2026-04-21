// qui-sync server — HTTP + web UI on top of core/.
//
// Runs alongside the CLI (cmd/cli) — both share the same core package.
// Default port 6070. Graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prophetse7en/qui-sync/api"
	"github.com/prophetse7en/qui-sync/core"
)

const version = "0.1.0-dev"

//go:embed web/static
var embeddedFS embed.FS

func main() {
	var (
		addr    = flag.String("addr", ":6070", "HTTP listen address")
		cfgPath = flag.String("config", core.DefaultConfigPath(), "Path to config.yml")
		webDir  = flag.String("web-dir", "", "Optional path to a web/static directory (dev override of embedded FS)")
	)
	flag.Parse()

	cfg, err := core.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if !cfg.IsQuiConfigured() {
		log.Println("Qui not configured yet — open the UI and go to Settings to complete setup.")
	}

	staticFS, err := resolveStatic(*webDir)
	if err != nil {
		log.Fatalf("static: %v", err)
	}

	srv, err := api.NewServer(*cfgPath, cfg, staticFS, version)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Auto-sync background worker.
	worker := core.NewAutoSyncWorker(srv.GetConfig, srv.GetConsumerState, srv.NewClient)
	go worker.Start(ctx)

	// Scheduled full-instance backups. The worker polls every minute
	// and dispatches when the cfg.Backup.Cron expression fires — only
	// when cfg.Backup.Enabled is true. Disabled by default until the
	// user configures it via Settings.
	backupScheduler := core.NewBackupScheduler(srv.GetConfig, srv.NewClient)
	go backupScheduler.Start(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("qui-sync server %s — listening on %s (config: %s)", version, *addr, *cfgPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

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
	sub, err := fs.Sub(embeddedFS, "web/static")
	if err != nil {
		return nil, err
	}
	return sub, nil
}
