package config

import (
	"fmt"
	"os"
	"strings"

	"tesla-charger-service/internal/anchor"
	"tesla-charger-service/internal/schedule"
)

const (
	tokenAuthURL  = "https://auth.tesla.com/oauth2/v3/authorize"
	tokenURL      = "https://auth.tesla.com/oauth2/v3/token"
	defaultPort   = "5000"
	defaultScopes = "offline_access vehicle_device_data vehicle_cmds"
	oauthCallback = "/oauth/callback"
)

type Config struct {
	TeslaClientID     string
	TeslaClientSecret string
	TeslaRedirectURI  string
	AppBaseURL        string
	TeslaVIN          string
	ChargingSchedule  schedule.Daily
	AnchorBaseURL     string
	AnchorAPIKey      string
	TeslaBaseURL      string
	Port              string
	Scopes            []string
	TeslaAuthURL      string
	TeslaTokenURL     string
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		TeslaClientID:     strings.TrimSpace(os.Getenv("TESLA_CLIENT_ID")),
		TeslaClientSecret: strings.TrimSpace(os.Getenv("TESLA_CLIENT_SECRET")),
		AppBaseURL:        strings.TrimSpace(os.Getenv("APP_BASE_URL")),
		TeslaVIN:          strings.TrimSpace(os.Getenv("TESLA_VIN")),
		AnchorBaseURL:     strings.TrimSpace(os.Getenv("ANCHOR_BASE_URL")),
		AnchorAPIKey:      strings.TrimSpace(os.Getenv("ANCHOR_API_KEY")),
		TeslaBaseURL:      strings.TrimSpace(os.Getenv("TESLA_BASE_URL")),
		Port:              strings.TrimSpace(os.Getenv("PORT")),
		TeslaAuthURL:      tokenAuthURL,
		TeslaTokenURL:     tokenURL,
	}
	clock := strings.TrimSpace(os.Getenv("CHARGING_CHECK_TIME"))
	if clock == "" {
		clock = "23:00"
	}
	timezone := strings.TrimSpace(os.Getenv("CHARGING_CHECK_TIMEZONE"))
	if timezone == "" {
		timezone = "America/New_York"
	}
	var err error
	cfg.ChargingSchedule, err = schedule.Parse(clock, timezone)
	if err != nil {
		return Config{}, err
	}

	if cfg.Port == "" {
		cfg.Port = defaultPort
	}

	if cfg.AppBaseURL != "" && !strings.HasPrefix(cfg.AppBaseURL, "http://") && !strings.HasPrefix(cfg.AppBaseURL, "https://") {
		cfg.AppBaseURL = "https://" + cfg.AppBaseURL
	}

	scopeValue := strings.TrimSpace(os.Getenv("TESLA_SCOPES"))
	if scopeValue == "" {
		scopeValue = defaultScopes
	}
	cfg.Scopes = strings.Fields(scopeValue)
	cfg.TeslaRedirectURI = strings.TrimRight(cfg.AppBaseURL, "/") + oauthCallback

	missing := make([]string, 0, 6)
	if cfg.TeslaClientID == "" {
		missing = append(missing, "TESLA_CLIENT_ID")
	}
	if cfg.TeslaClientSecret == "" {
		missing = append(missing, "TESLA_CLIENT_SECRET")
	}
	if cfg.AppBaseURL == "" {
		missing = append(missing, "APP_BASE_URL")
	}
	if cfg.TeslaVIN == "" {
		missing = append(missing, "TESLA_VIN")
	}
	if cfg.AnchorBaseURL == "" {
		missing = append(missing, "ANCHOR_BASE_URL")
	}
	if cfg.AnchorAPIKey == "" {
		missing = append(missing, "ANCHOR_API_KEY")
	}
	if cfg.TeslaBaseURL == "" {
		missing = append(missing, "TESLA_BASE_URL")
	}

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	if err := anchor.ValidateURL(cfg.AnchorBaseURL); err != nil {
		return Config{}, err
	}

	return cfg, nil
}
