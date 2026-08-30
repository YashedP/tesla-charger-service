package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger"
	"golang.org/x/oauth2"

	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/paths"

	_ "tesla-charger-service/docs"
)

const (
	oauthStateCookieName = "oauth_state"
	requestTimeout       = 45 * time.Second
)

type Server struct {
	cfg      config.Config
	oauthCfg *oauth2.Config
	tokens   tokenExchanger
	logger   *slog.Logger
}

type tokenExchanger interface {
	Exchange(context.Context, string, string) error
}

func NewRouter(cfg config.Config, oauthCfg *oauth2.Config, tokens tokenExchanger, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{
		cfg:      cfg,
		oauthCfg: oauthCfg,
		tokens:   tokens,
		logger:   logger,
	}

	r := chi.NewRouter()
	r.Use(s.requestLogger)
	r.Get("/health", s.handleHealth)
	r.Get("/.well-known/appspecific/com.tesla.3p.public-key.pem", s.handleFleetPublicKey)
	r.Get("/oauth/start", s.handleOAuthStart)
	r.Get("/oauth/callback", s.handleOAuthCallback)
	r.Get("/docs", http.RedirectHandler("/docs/", http.StatusMovedPermanently).ServeHTTP)
	r.Get("/docs/*", httpSwagger.Handler(
		httpSwagger.URL("/docs/doc.json"),
	))

	return r
}

// @Summary Health check
// @Description Returns OK when the HTTP process is running.
// @Tags health
// @Produce plain
// @Success 200 {string} string "ok"
// @Router /health [get]
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

// @Summary Serve Fleet API EC public key
// @Description Returns the EC public key PEM used for Tesla Fleet API partner registration. Tesla fetches this endpoint unauthenticated.
// @Tags fleet
// @Produce octet-stream
// @Success 200 {string} string "PEM-encoded EC public key"
// @Failure 404 {string} string "public key not found"
// @Router /.well-known/appspecific/com.tesla.3p.public-key.pem [get]
func (s *Server) handleFleetPublicKey(w http.ResponseWriter, r *http.Request) {
	pem, err := os.ReadFile(paths.FleetECPublicKeyPath)
	if err != nil {
		s.log(r, slog.LevelWarn, "fleet_public_key_missing")
		http.Error(w, "public key not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pem)
}

// @Summary Start Tesla OAuth flow
// @Description Redirects the user to Tesla's OAuth authorization page. Sets a state cookie for CSRF protection.
// @Tags oauth
// @Produce plain
// @Success 302 {string} string "Redirect to Tesla OAuth"
// @Failure 500 {string} string "internal error"
// @Router /oauth/start [get]
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	state, err := randomState(24)
	if err != nil {
		s.log(r, slog.LevelError, "oauth_start_failed", slog.String("error", safeError(err)))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    state,
		Path:     "/oauth/callback",
		HttpOnly: true,
		MaxAge:   int((10 * time.Minute).Seconds()),
		SameSite: http.SameSiteLaxMode,
	})

	authURL := s.oauthCfg.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("audience", s.cfg.TeslaBaseURL),
	)
	s.log(r, slog.LevelInfo, "oauth_start_redirect")
	http.Redirect(w, r, authURL, http.StatusFound)
}

// @Summary Handle Tesla OAuth callback
// @Description Exchanges the authorization code for tokens and stores them encrypted in SQLite.
// @Tags oauth
// @Produce plain
// @Param state query string true "OAuth state parameter"
// @Param code query string true "Authorization code"
// @Success 200 {string} string "OAuth successful"
// @Failure 400 {string} string "missing state, code, or cookie"
// @Failure 500 {string} string "exchange or persistence error"
// @Router /oauth/callback [get]
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		s.log(r, slog.LevelWarn, "oauth_callback_invalid", slog.String("reason", "missing_state"))
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.log(r, slog.LevelWarn, "oauth_callback_invalid", slog.String("reason", "missing_code"))
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	stateCookie, err := r.Cookie(oauthStateCookieName)
	if err != nil {
		s.log(r, slog.LevelWarn, "oauth_callback_invalid", slog.String("reason", "missing_state_cookie"))
		http.Error(w, "missing oauth state cookie", http.StatusBadRequest)
		return
	}
	if !secureEquals(stateCookie.Value, state) {
		s.log(r, slog.LevelWarn, "oauth_callback_invalid", slog.String("reason", "invalid_state"))
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	s.log(r, slog.LevelInfo, "oauth_token_exchange_start")
	if err := s.tokens.Exchange(ctx, code, s.cfg.TeslaBaseURL); err != nil {
		s.log(r, slog.LevelError, "oauth_token_exchange_failed", slog.String("error", safeError(err)))
		http.Error(w, "oauth exchange failed", http.StatusInternalServerError)
		return
	}

	s.log(r, slog.LevelInfo, "oauth_callback_complete")

	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     "/oauth/callback",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "OAuth successful. Nightly charging checks are ready.\n")
}

func secureEquals(a string, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomState(size int) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
