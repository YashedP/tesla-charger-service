package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger"
	"golang.org/x/oauth2"

	"tesla-charger-status/internal/config"
	"tesla-charger-status/internal/store"

	_ "tesla-charger-status/docs"
)

const (
	oauthStateCookieName = "oauth_state"
	requestTimeout       = 20 * time.Second
)

type TokenStore interface {
	LoadToken(ctx context.Context) (*oauth2.Token, error)
	SaveToken(ctx context.Context, token *oauth2.Token) error
}

type TeslaClient interface {
	GetChargingState(ctx context.Context, httpClient *http.Client, vin string) (string, error)
}

type Server struct {
	cfg      config.Config
	oauthCfg *oauth2.Config
	tokens   TokenStore
	tesla    TeslaClient
	logger   *log.Logger
}

func NewRouter(cfg config.Config, oauthCfg *oauth2.Config, tokens TokenStore, tesla TeslaClient, logger *log.Logger) http.Handler {
	s := &Server{
		cfg:      cfg,
		oauthCfg: oauthCfg,
		tokens:   tokens,
		tesla:    tesla,
		logger:   logger,
	}

	r := chi.NewRouter()
	r.Get("/oauth/start", s.handleOAuthStart)
	r.Get("/oauth/callback", s.handleOAuthCallback)
	r.Get("/v1/is-charging", s.handleIsCharging)
	r.Get("/docs/*", httpSwagger.Handler(
		httpSwagger.URL("/docs/doc.json"),
	))

	return r
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
		s.logger.Printf("oauth start: generate state: %v", err)
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
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	stateCookie, err := r.Cookie(oauthStateCookieName)
	if err != nil {
		http.Error(w, "missing oauth state cookie", http.StatusBadRequest)
		return
	}
	if !secureEquals(stateCookie.Value, state) {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	tok, err := s.oauthCfg.Exchange(ctx, code, oauth2.SetAuthURLParam("audience", s.cfg.TeslaBaseURL))
	if err != nil {
		s.logger.Printf("oauth callback: exchange code: %v", err)
		http.Error(w, "oauth exchange failed", http.StatusInternalServerError)
		return
	}

	if err := s.tokens.SaveToken(ctx, tok); err != nil {
		s.logger.Printf("oauth callback: save token: %v", err)
		http.Error(w, "token persistence failed", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookieName,
		Value:    "",
		Path:     "/oauth/callback",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "OAuth successful. You can now call /v1/is-charging.\n")
}

// @Summary Check if vehicle is charging
// @Description Returns "true" or "false" indicating whether the configured Tesla vehicle is currently charging. Errors map to "false".
// @Tags charging
// @Produce plain
// @Security BearerAuth
// @Success 200 {string} string "true or false"
// @Failure 401 {string} string "unauthorized"
// @Router /v1/is-charging [get]
func (s *Server) handleIsCharging(w http.ResponseWriter, r *http.Request) {
	if !validBearer(r.Header.Get("Authorization"), s.cfg.ShortcutBearerToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	tok, err := s.tokens.LoadToken(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrTokenNotFound) {
			s.logger.Printf("is-charging: load token: %v", err)
		}
		s.writeBool(w, false)
		return
	}

	src := s.oauthCfg.TokenSource(ctx, tok)
	fresh, err := src.Token()
	if err != nil {
		s.logger.Printf("is-charging: refresh token: %v", err)
		s.writeBool(w, false)
		return
	}

	if fresh.RefreshToken == "" {
		fresh.RefreshToken = tok.RefreshToken
	}
	if tokenChanged(tok, fresh) {
		if err := s.tokens.SaveToken(ctx, fresh); err != nil {
			s.logger.Printf("is-charging: save refreshed token: %v", err)
		}
	}

	httpClient := s.oauthCfg.Client(ctx, fresh)
	state, err := s.tesla.GetChargingState(ctx, httpClient, s.cfg.TeslaVIN)
	if err != nil {
		s.logger.Printf("is-charging: tesla status: %v", err)
		s.writeBool(w, false)
		return
	}

	s.writeBool(w, strings.EqualFold(state, "Charging"))
}

func (s *Server) writeBool(w http.ResponseWriter, value bool) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if value {
		_, _ = io.WriteString(w, "true")
		return
	}
	_, _ = io.WriteString(w, "false")
}

func validBearer(header string, expectedToken string) bool {
	const bearerPrefix = "bearer "

	header = strings.TrimSpace(header)
	if len(header) < len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return false
	}
	provided := strings.TrimSpace(header[len(bearerPrefix):])
	return secureEquals(provided, expectedToken)
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

func tokenChanged(oldTok *oauth2.Token, newTok *oauth2.Token) bool {
	if oldTok == nil || newTok == nil {
		return true
	}
	return oldTok.AccessToken != newTok.AccessToken ||
		oldTok.RefreshToken != newTok.RefreshToken ||
		oldTok.TokenType != newTok.TokenType ||
		!oldTok.Expiry.Equal(newTok.Expiry)
}
