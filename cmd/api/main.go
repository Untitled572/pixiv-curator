package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
	"github.com/Untitled572/pixiv-curator/internal/secret"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"

	"github.com/Untitled572/pixiv-curator/internal/config"
	"github.com/Untitled572/pixiv-curator/internal/httpapi"
)

// version can be overridden using go build -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := start(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func start() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, logger, cfg)
}

func run(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	store, err := sqlite.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open catalog: %w", err)
	}
	defer store.Close()
	var token string
	if cfg.AdminTokenFile != "" {
		token, err = secret.Read(cfg.AdminTokenFile)
		if err != nil {
			return fmt.Errorf("read admin credential: %w", err)
		}
	}
	auth := httpapi.NewAuth([]byte(token), httpapi.AuthOptions{PublicOrigin: cfg.PublicBaseURL, SecureCookies: len(cfg.PublicBaseURL) >= 8 && cfg.PublicBaseURL[:8] == "https://"})
	service := app.NewCatalog(store)
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	// Before Pixiv is configured, accepted collection work waits for credentials.
	unavailable := func(context.Context, domain.Job) error {
		return &jobs.JobError{Class: jobs.PausedAuth, Code: "ACCOUNT_NOT_CONFIGURED"}
	}
	worker := jobs.NewWorker(store, map[string]jobs.Handler{"sync_artist": unavailable, "sync_following": unavailable, "hydrate_artwork": unavailable, "repair_media": unavailable, "recommend_artists": unavailable})
	go func() { defer close(workerDone); _ = worker.Run(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	server := &http.Server{
		Handler: httpapi.NewHandlerWithRoutes(version, func(mux *http.ServeMux) {
			auth.Register(mux)
			mux.Handle("/admin/", auth.Middleware(httpapi.NewAdminHandler(service)))
			mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
				checkCtx, cancel := context.WithTimeout(r.Context(), time.Second)
				defer cancel()
				ready := store.Ping(checkCtx) == nil
				select {
				case <-workerDone:
					ready = false
				default:
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				if !ready {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"code":"NOT_READY"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"status":"ready"}`))
			})
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	defer server.Close()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	logger.Info("server started", "addr", listener.Addr().String(), "version", version)

	select {
	case err := <-serveErr:
		return checkServeError(err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return checkServeError(<-serveErr)
}

func checkServeError(err error) error {
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}
