package config

import (
	"strings"
	"testing"
)

func TestChargingConfiguration(t *testing.T) {
	for key, value := range map[string]string{"TESLA_CLIENT_ID": "test-id", "TESLA_CLIENT_SECRET": "test-secret", "APP_BASE_URL": "https://tesla.example", "TESLA_VIN": "test-vin", "TESLA_BASE_URL": "https://fleet.example", "ANCHOR_BASE_URL": "https://anchor.example", "ANCHOR_API_KEY": "test-key", "CHARGING_CHECK_TIME": "", "CHARGING_CHECK_TIMEZONE": "", "TESLA_SCOPES": ""} {
		t.Setenv(key, value)
	}
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChargingSchedule.Clock != "23:00" || cfg.ChargingSchedule.Location.String() != "America/New_York" {
		t.Fatal("wrong schedule defaults")
	}
	if !strings.Contains(strings.Join(cfg.Scopes, " "), "vehicle_cmds") {
		t.Fatal("wake-up scope missing")
	}
	t.Setenv("CHARGING_CHECK_TIME", "06:45")
	t.Setenv("CHARGING_CHECK_TIMEZONE", "Europe/London")
	cfg, err = LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ChargingSchedule.Clock != "06:45" || cfg.ChargingSchedule.Location.String() != "Europe/London" {
		t.Fatal("overrides ignored")
	}
	t.Setenv("ANCHOR_BASE_URL", "https://user:private-secret@anchor.example")
	if _, err := LoadFromEnv(); err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("unsafe Anchor URL validation")
	}
	t.Setenv("ANCHOR_BASE_URL", "https://anchor.example")
	t.Setenv("ANCHOR_API_KEY", "")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("missing Anchor key accepted")
	}
}
