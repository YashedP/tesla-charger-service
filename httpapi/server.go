package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger"
	"golang.org/x/oauth2"

	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/paths"
	"tesla-charger-service/internal/store"
	"tesla-charger-service/internal/tesla"

	_ "tesla-charger-service/docs"
)

const (
	oauthStateCookieName = "oauth_state"
	requestTimeout       = 45 * time.Second
	wakePollInterval     = 2 * time.Second
)

type TokenStore interface {
	LoadToken(ctx context.Context) (*oauth2.Token, error)
	SaveToken(ctx context.Context, token *oauth2.Token) error
}

type TeslaClient interface {
	GetChargingState(ctx context.Context, httpClient *http.Client, vin string) (string, error)
	WakeUp(ctx context.Context, httpClient *http.Client, vin string) error
	GetVehicleState(ctx context.Context, httpClient *http.Client, vin string) (string, error)
}

// ChargingResponse represents the /v1/is-charging response.
type ChargingResponse struct {
	IsCharging bool `json:"is_charging"`
}

// ErrorResponse represents an API error.
type ErrorResponse struct {
	Error string `json:"error"`
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
	r.Get("/.well-known/appspecific/com.tesla.3p.public-key.pem", s.handleFleetPublicKey)
	r.Get("/oauth/start", s.handleOAuthStart)
	r.Get("/oauth/callback", s.handleOAuthCallback)
	r.Get("/v1/is-charging", s.handleIsCharging)
	r.Get("/docs", http.RedirectHandler("/docs/", http.StatusMovedPermanently).ServeHTTP)
	r.Get("/docs/*", httpSwagger.Handler(
		httpSwagger.URL("/docs/doc.json"),
	))

	return r
}

// @Summary Serve Fleet API EC public key
// @Description Returns the EC public key PEM used for Tesla Fleet API partner registration. Tesla fetches this endpoint unauthenticated.
// @Tags fleet
// @Produce octet-stream
// @Success 200 {string} string "PEM-encoded EC public key"
// @Failure 404 {string} string "public key not found"
// @Router /.well-known/appspecific/com.tesla.3p.public-key.pem [get]
func (s *Server) handleFleetPublicKey(w http.ResponseWriter, _ *http.Request) {
	pem, err := os.ReadFile(paths.FleetECPublicKeyPath)
	if err != nil {
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
// @Description Returns whether the configured Tesla vehicle is currently charging. Wakes the vehicle if asleep. Defaults to is_charging=true on unrecoverable errors (safe default to prevent false alarms).
// @Tags charging
// @Produce json
// @Security BearerAuth
// @Success 200 {object} ChargingResponse
// @Failure 401 {object} ErrorResponse
// @Router /v1/is-charging [get]
func (s *Server) handleIsCharging(w http.ResponseWriter, r *http.Request) {
	if !validBearer(r.Header.Get("Authorization"), s.cfg.ShortcutBearerToken) {
		s.writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	tok, err := s.tokens.LoadToken(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrTokenNotFound) {
			s.logger.Printf("is-charging: load token: %v", err)
		}
		s.writeJSON(w, http.StatusOK, ChargingResponse{IsCharging: false})
		return
	}

	src := s.oauthCfg.TokenSource(ctx, tok)
	fresh, err := src.Token()
	if err != nil {
		s.logger.Printf("is-charging: refresh token: %v", err)
		s.writeJSON(w, http.StatusOK, ChargingResponse{IsCharging: false})
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
		if errors.Is(err, tesla.ErrVehicleUnavailable) {
			s.logger.Printf("is-charging: vehicle asleep, attempting wake")
			state, err = tesla.WakeAndGetChargingState(ctx, s.tesla, httpClient, s.cfg.TeslaVIN, wakePollInterval)
			if err != nil {
				s.logger.Printf("is-charging: wake failed: %v — defaulting to charging=true", err)
				s.writeJSON(w, http.StatusOK, ChargingResponse{IsCharging: true})
				return
			}
		} else {
			s.logger.Printf("is-charging: tesla status: %v — defaulting to charging=true", err)
			s.writeJSON(w, http.StatusOK, ChargingResponse{IsCharging: true})
			return
		}
	}

	isCharging := strings.EqualFold(state, "Charging") || strings.EqualFold(state, "Complete")
	s.writeJSON(w, http.StatusOK, ChargingResponse{IsCharging: isCharging})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Printf("writeJSON: %v", err)
	}
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
