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
	"tesla-charger-service/internal/telemetry"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"

	"tesla-charger-service/httpapi"
	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/crypto"
	"tesla-charger-service/internal/paths"
	"tesla-charger-service/internal/store"
	"tesla-charger-service/internal/tesla"
)

const privateDirPerm os.FileMode = 0o700

// @title Tesla Charger Status API
// @version 1.0
// @description Service that wraps the Tesla Fleet API to report vehicle charging state.
// @host localhost:5000
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Best-effort load for local development. Existing process env vars are preserved.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fatal(logger, "load .env file", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTelemetry, err := telemetry.Init(ctx, "tesla-charger-service")
	if err != nil {
		fatal(logger, "initialize telemetry", err)
	}
	defer shutdownTelemetry()

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

	tokenStore, err := store.NewSQLiteTokenStore(paths.SQLitePath, cipher)
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
	handler := httpapi.NewRouter(cfg, oauthCfg, tokenStore, fleetClient, logger)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server_shutdown_failed")
		}
	}()

	logger.Info("server_starting", slog.String("addr", server.Addr))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(logger, "server failure", err)
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
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
