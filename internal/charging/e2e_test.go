package charging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"tesla-charger-service/httpapi"
	"tesla-charger-service/internal/anchor"
	"tesla-charger-service/internal/config"
	"tesla-charger-service/internal/schedule"
	"tesla-charger-service/internal/store"
	"tesla-charger-service/internal/tesla"
)

// This runs the real HTTP router, OAuth exchange, encrypted SQLite store, Fleet
// client, scheduling loop, and Anchor client against local HTTP providers only.
func TestScheduledAlertE2ERestartAfterLostResponse(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "test.sqlite")
	db := newTestStore(t, path)
	var fleetCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != "/api/1/vehicles/fixture-vin/vehicle_data" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Error("incorrect Fleet request")
			w.WriteHeader(400)
			return
		}
		fleetCalls.Add(1)
		_, _ = w.Write([]byte(`{"response":{"charge_state":{"charging_state":"Disconnected"}}}`))
	}))
	defer provider.Close()
	oauth := &oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: provider.URL + "/authorize", TokenURL: provider.URL + "/token"}}
	tokens := tesla.NewTokens(db, oauth)
	api := httptest.NewServer(httpapi.NewRouter(config.Config{TeslaBaseURL: provider.URL}, oauth, tokens, logger))
	defer api.Close()
	req, err := http.NewRequest("GET", api.URL+"/oauth/callback?state=fixture-state&code=fixture-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "fixture-state"})
	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("OAuth setup failed")
	}
	var mu sync.Mutex
	var payloads []string
	anchorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		payloads = append(payloads, string(body))
		first := len(payloads) == 1
		mu.Unlock()
		if first {
			// Anchor accepted the request, but the TCP connection died before its response.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK) // Identical idempotent replay.
	}))
	defer anchorServer.Close()
	notifications, err := anchor.NewClient(anchorServer.URL, "fixture-anchor-key")
	if err != nil {
		t.Fatal(err)
	}
	daily, err := schedule.Parse("23:00", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	check := NewChecker(tokens, tesla.NewFleetClient(provider.URL), "fixture-vin", logger)
	w := NewWorker(daily, "fixture-vin", db, check, notifications, logger)
	w.clock = &fakeClock{now: scheduledInstant().Add(-time.Second), stopAt: scheduledInstant().Add(time.Second)}
	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if readRun(t, path).State != store.RunPending {
		t.Fatal("ambiguous send was not retained")
	}
	api.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen persisted state and reconstruct all service dependencies as on restart.
	reopened := newTestStore(t, path)
	notifications, err = anchor.NewClient(anchorServer.URL, "fixture-anchor-key")
	if err != nil {
		t.Fatal(err)
	}
	check = NewChecker(tesla.NewTokens(reopened, oauth), tesla.NewFleetClient(provider.URL), "fixture-vin", logger)
	restarted := NewWorker(daily, "fixture-vin", reopened, check, notifications, logger)
	restarted.clock = &fakeClock{now: scheduledInstant().Add(10 * time.Second), stopAt: scheduledInstant().Add(time.Minute)}
	if err := restarted.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if readRun(t, path).State != store.RunAccepted {
		t.Fatal("replay did not complete")
	}
	step(t, restarted)
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 2 || payloads[0] != payloads[1] || fleetCalls.Load() != 1 {
		t.Fatal("restart duplicated the check or changed notification identity")
	}
}
