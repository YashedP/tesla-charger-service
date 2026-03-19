package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"

	"tesla-charger-status/internal/config"
	"tesla-charger-status/internal/crypto"
	"tesla-charger-status/httpapi"
	"tesla-charger-status/internal/paths"
	"tesla-charger-status/internal/store"
	"tesla-charger-status/internal/tesla"
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
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	// Best-effort load for local development. Existing process env vars are preserved.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		logger.Fatalf("load .env file: %v", err)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	if err := ensureParentDirs(paths.SQLitePath, paths.KeyPath); err != nil {
		logger.Fatalf("prepare filesystem: %v", err)
	}

	key, err := crypto.LoadKeyFromFile(paths.KeyPath)
	if err != nil {
		logger.Fatalf("load encryption key from %s: %v", paths.KeyPath, err)
	}

	cipher, err := crypto.NewAESCipher(key)
	if err != nil {
		logger.Fatalf("initialize encryption cipher: %v", err)
	}

	tokenStore, err := store.NewSQLiteTokenStore(paths.SQLitePath, cipher)
	if err != nil {
		logger.Fatalf("initialize token store at %s: %v", paths.SQLitePath, err)
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
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Printf("starting server on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server failure: %v", err)
	}
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
