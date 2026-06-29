package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"

	"golang.org/x/oauth2"

	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/store"
	"tesla-charger-service/internal/tesla"
)

type mockTokenStore struct {
	token   *oauth2.Token
	loadErr error
	saveErr error
}

func (m *mockTokenStore) LoadToken(context.Context) (*oauth2.Token, error) {
	return m.token, m.loadErr
}

func (m *mockTokenStore) SaveToken(context.Context, *oauth2.Token) error {
	return m.saveErr
}

type mockTeslaClient struct {
	chargingState string
	chargingErr   error
	wakeErr       error
	vehicleStates []string
	vehicleErr    error
	vehicleCalls  int
}

func (m *mockTeslaClient) GetChargingState(context.Context, *http.Client, string) (string, error) {
	if m.chargingErr != nil {
		err := m.chargingErr
		m.chargingErr = nil
		return "", err
	}
	return m.chargingState, nil
}

func (m *mockTeslaClient) WakeUp(context.Context, *http.Client, string) error {
	return m.wakeErr
}

func (m *mockTeslaClient) GetVehicleState(context.Context, *http.Client, string) (string, error) {
	m.vehicleCalls++
	if m.vehicleErr != nil {
		return "", m.vehicleErr
	}
	if m.vehicleCalls > len(m.vehicleStates) {
		return m.vehicleStates[len(m.vehicleStates)-1], nil
	}
	return m.vehicleStates[m.vehicleCalls-1], nil
}

func TestIsChargingLogsUnauthorized(t *testing.T) {
	events, rec := exerciseIsCharging(t, &mockTokenStore{}, &mockTeslaClient{}, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	assertEvent(t, events, "is_charging_start")
	assertEvent(t, events, "shortcut_auth_failed")
	assertEvent(t, events, "request_complete")
	assertSharedRequestID(t, events)
}

func TestRequestLoggerDumpsSanitizedHeaders(t *testing.T) {
	events, rec := exerciseIsChargingWithHeaders(t, &mockTokenStore{}, &mockTeslaClient{}, http.Header{
		"Authorization":    {"Bearer wrong-token"},
		"CF-Connecting-IP": {"203.0.113.10"},
		"X-Debug-Token":    {"debug-token"},
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	received := assertEvent(t, events, "request_received")
	headers, ok := received["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers = %#v, want object", received["headers"])
	}

	assertHeaderValues(t, headers, http.CanonicalHeaderKey("Authorization"), []string{"REDACTED"})
	assertHeaderValues(t, headers, http.CanonicalHeaderKey("CF-Connecting-IP"), []string{"203.0.113.10"})
	assertHeaderValues(t, headers, http.CanonicalHeaderKey("X-Debug-Token"), []string{"REDACTED"})
}

func TestIsChargingLogsTokenMissing(t *testing.T) {
	events, rec := exerciseIsCharging(t, &mockTokenStore{loadErr: store.ErrTokenNotFound}, &mockTeslaClient{}, "Bearer test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertEvent(t, events, "token_load_start")
	tokenMissing := assertEvent(t, events, "token_missing")
	if tokenMissing["result"] != "not_charging" {
		t.Fatalf("token_missing result = %v, want not_charging", tokenMissing["result"])
	}
	assertSharedRequestID(t, events)
}

func TestIsChargingLogsSuccess(t *testing.T) {
	events, rec := exerciseIsCharging(t, validTokenStore(), &mockTeslaClient{chargingState: "Charging"}, "Bearer test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertEvent(t, events, "tesla_charge_state_start")
	complete := assertEvent(t, events, "is_charging_complete")
	if complete["is_charging"] != true {
		t.Fatalf("is_charging = %v, want true", complete["is_charging"])
	}
	assertSharedRequestID(t, events)
}

func TestIsChargingLogsWakeSuccess(t *testing.T) {
	events, rec := exerciseIsCharging(t, validTokenStore(), &mockTeslaClient{
		chargingErr:   tesla.ErrVehicleUnavailable,
		chargingState: "Complete",
		vehicleStates: []string{"asleep", "online"},
	}, "Bearer test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertEvent(t, events, "vehicle_asleep_wake_start")
	assertEvent(t, events, "vehicle_wake_command_sent")
	assertEvent(t, events, "vehicle_online")
	complete := assertEvent(t, events, "is_charging_complete")
	if complete["tesla_state"] != "Complete" {
		t.Fatalf("tesla_state = %v, want Complete", complete["tesla_state"])
	}
	assertSharedRequestID(t, events)
}

func TestIsChargingLogsWakeFailure(t *testing.T) {
	events, rec := exerciseIsCharging(t, validTokenStore(), &mockTeslaClient{
		chargingErr: tesla.ErrVehicleUnavailable,
		wakeErr:     context.DeadlineExceeded,
	}, "Bearer test-token")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertEvent(t, events, "vehicle_wake_command_failed")
	failed := assertEvent(t, events, "vehicle_wake_failed")
	if failed["result"] != "default_charging_true" {
		t.Fatalf("vehicle_wake_failed result = %v, want default_charging_true", failed["result"])
	}
	assertSharedRequestID(t, events)
}

func exerciseIsCharging(t *testing.T, tokens store.TokenStore, teslaClient tesla.Client, authHeader string) ([]map[string]any, *httptest.ResponseRecorder) {
	t.Helper()

	headers := http.Header{}
	if authHeader != "" {
		headers.Set("Authorization", authHeader)
	}
	return exerciseIsChargingWithHeaders(t, tokens, teslaClient, headers)
}

func exerciseIsChargingWithHeaders(t *testing.T, tokens store.TokenStore, teslaClient tesla.Client, headers http.Header) ([]map[string]any, *httptest.ResponseRecorder) {
	t.Helper()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := NewRouter(config.Config{
		ShortcutBearerToken: "test-token",
		TeslaVIN:            "vin-not-logged",
	}, &oauth2.Config{}, tokens, teslaClient, logger)

	req := httptest.NewRequest(http.MethodGet, "/v1/is-charging", nil)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return parseLogEvents(t, logs.Bytes()), rec
}

func validTokenStore() *mockTokenStore {
	return &mockTokenStore{token: &oauth2.Token{
		AccessToken:  "access-token-not-logged",
		RefreshToken: "refresh-token-not-logged",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}}
}

func parseLogEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func assertHeaderValues(t *testing.T, headers map[string]any, key string, want []string) {
	t.Helper()

	rawValues, ok := headers[key].([]any)
	if !ok {
		t.Fatalf("headers[%q] = %#v, want array", key, headers[key])
	}
	if len(rawValues) != len(want) {
		t.Fatalf("headers[%q] len = %d, want %d", key, len(rawValues), len(want))
	}
	for i, rawValue := range rawValues {
		value, ok := rawValue.(string)
		if !ok {
			t.Fatalf("headers[%q][%d] = %#v, want string", key, i, rawValue)
		}
		if value != want[i] {
			t.Fatalf("headers[%q][%d] = %q, want %q", key, i, value, want[i])
		}
	}
}

func assertEvent(t *testing.T, events []map[string]any, name string) map[string]any {
	t.Helper()

	for _, event := range events {
		if event["event"] == name {
			return event
		}
	}
	t.Fatalf("missing log event %q in %#v", name, events)
	return nil
}

func assertSharedRequestID(t *testing.T, events []map[string]any) {
	t.Helper()

	var requestID any
	for _, event := range events {
		id := event["request_id"]
		if id == nil {
			continue
		}
		if requestID == nil {
			requestID = id
			continue
		}
		if id != requestID {
			t.Fatalf("request_id mismatch: got %v, want %v", id, requestID)
		}
	}
	if requestID == nil {
		t.Fatalf("no request_id found in events: %#v", events)
	}
}
