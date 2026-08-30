package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"

	"tesla-charger-service/httpapi"
	"tesla-charger-service/internal/anchor"
	"tesla-charger-service/internal/charging"
	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/crypto"
	"tesla-charger-service/internal/paths"
	"tesla-charger-service/internal/store"
	"tesla-charger-service/internal/tesla"
)

// @title Tesla Charging Monitor
// @version 1.0
// @description Nightly charging monitor. HTTP endpoints support Tesla OAuth, Fleet registration, and health checks.
// @host localhost:5000
// @BasePath /
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Best-effort load for local development. Existing process env vars are preserved.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fatal(logger, "load .env file", err)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fatal(logger, "load config", err)
	}

	if err := ensureParentDirs(paths.SQLitePath, paths.KeyPath); err != nil {
		fatal(logger, "prepare filesystem", err)
	}

	key, err := crypto.LoadKeyFromFile(paths.KeyPath)
	if err != nil {
		fatal(logger, "load encryption key", err, slog.String("path", paths.KeyPath))
	}

	cipher, err := crypto.NewAESCipher(key)
	if err != nil {
		fatal(logger, "initialize encryption cipher", err)
	}

	tokenStore, err := store.NewSQLiteStore(paths.SQLitePath, cipher)
	if err != nil {
		fatal(logger, "initialize token store", err, slog.String("path", paths.SQLitePath))
	}
	defer func() { _ = tokenStore.Close() }()

	oauthCfg := &oauth2.Config{
		ClientID:     cfg.TeslaClientID,
		ClientSecret: cfg.TeslaClientSecret,
		RedirectURL:  cfg.TeslaRedirectURI,
		Scopes:       cfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  cfg.TeslaAuthURL,
			TokenURL: cfg.TeslaTokenURL,
		},
	}

	fleetClient := tesla.NewFleetClient(cfg.TeslaBaseURL)
	tokens := tesla.NewTokens(tokenStore, oauthCfg)
	handler := httpapi.NewRouter(cfg, oauthCfg, tokens, logger)
	notifications, err := anchor.NewClient(cfg.AnchorBaseURL, cfg.AnchorAPIKey)
	if err != nil {
		fatal(logger, "initialize Anchor client", err)
	}
	checker := charging.NewChecker(tokens, fleetClient, cfg.TeslaVIN, logger)
	worker := charging.NewWorker(cfg.ChargingSchedule, cfg.TeslaVIN, tokenStore, checker, notifications, logger)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Info("server_starting", slog.String("addr", server.Addr))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, server, worker.Run); err != nil {
		fatal(logger, "service failure", err)
	}
}

// run supervises both components. Either component exiting unexpectedly stops
// the other and fails the process, allowing the container restart policy to act.
func run(ctx context.Context, server *http.Server, worker func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverDone, workerDone := make(chan error, 1), make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	go func() { workerDone <- worker(ctx) }()
	var failure error
	serverExited, workerExited := false, false
	select {
	case <-ctx.Done():
	case err := <-serverDone:
		serverExited = true
		if ctx.Err() == nil {
			failure = fmt.Errorf("HTTP server stopped: %w", err)
		}
	case err := <-workerDone:
		workerExited = true
		if ctx.Err() == nil {
			failure = fmt.Errorf("charging worker stopped: %v", err)
		}
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		failure = errors.Join(failure, fmt.Errorf("HTTP shutdown: %w", err))
	}
	if !serverExited {
		if err := <-serverDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			failure = errors.Join(failure, err)
		}
	}
	if !workerExited {
		select {
		case err := <-workerDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				failure = errors.Join(failure, err)
			}
		case <-shutdownCtx.Done():
			failure = errors.Join(failure, fmt.Errorf("worker shutdown timed out"))
		}
	}
	return failure
}

func fatal(logger *slog.Logger, msg string, err error, attrs ...slog.Attr) {
	args := make([]any, 0, 2+len(attrs))
	args = append(args, slog.String("event", "startup_failed"), slog.String("error", err.Error()))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	logger.Error(msg, args...)
	os.Exit(1)
}

func ensureParentDirs(pathsToPrepare ...string) error {
	const privateDirPerm os.FileMode = 0o700
	// Create local runtime directories (for SQLite and secrets) if they're missing.
	for _, p := range pathsToPrepare {
		parent := filepath.Dir(p)
		if parent == "." {
			continue
		}
		// Restrict directory access to the current user.
		if err := os.MkdirAll(parent, privateDirPerm); err != nil {
			return fmt.Errorf("mkdir %s: %w", parent, err)
		}
	}
	return nil
}
