package charging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"tesla-charger-service/internal/tesla"
)

type fixedToken struct{ err error }

func (f fixedToken) Fresh(context.Context) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "fixture-access", TokenType: "Bearer"}, f.err
}

func TestCheckerStatesAndWake(t *testing.T) {
	for _, tc := range []struct {
		name, state, outcome string
		asleep               bool
		status               int
	}{
		{"charging", "Charging", Healthy, false, 200},
		{"complete", "Complete", Healthy, false, 200},
		{"stopped", "Stopped", NotCharging, false, 200},
		{"disconnected", "Disconnected", NotCharging, false, 200},
		{"no power", "NoPower", NotCharging, false, 200},
		{"missing", "", Unknown, false, 200},
		{"new state", "FutureState", Unknown, false, 200},
		{"HTTP failure", "", Unknown, false, 401},
		{"wake success", "Complete", Healthy, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var awake atomic.Bool
			awake.Store(!tc.asleep)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer fixture-access" {
					t.Error("missing OAuth bearer")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/wake_up"):
					awake.Store(true)
					_, _ = w.Write([]byte(`{"response":{"state":"online"}}`))
				case strings.HasSuffix(r.URL.Path, "/vehicle_data"):
					if !awake.Load() {
						w.WriteHeader(408)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"response":{"charge_state":{"charging_state":"` + tc.state + `"}}}`))
				default:
					_, _ = w.Write([]byte(`{"response":{"state":"online"}}`))
				}
			}))
			defer server.Close()
			c := NewChecker(fixedToken{}, tesla.NewFleetClient(server.URL), "fixture-vin", slog.New(slog.NewJSONHandler(io.Discard, nil)))
			c.pollInterval = time.Millisecond
			got := c.Check(context.Background())
			if got.Outcome != tc.outcome {
				t.Fatalf("got=%+v", got)
			}
		})
	}
}

func TestTokenFailureIsUnknown(t *testing.T) {
	c := NewChecker(fixedToken{err: errors.New("secret token failure")}, nil, "fixture-vin", slog.Default())
	if result := c.Check(context.Background()); result.Outcome != Unknown || result.Reason != "token_unavailable" {
		t.Fatalf("result=%+v", result)
	}
}

func TestWakeTimeoutIsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "vehicle_data") {
			w.WriteHeader(408)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"state":"asleep"}}`))
	}))
	defer server.Close()
	c := NewChecker(fixedToken{}, tesla.NewFleetClient(server.URL), "fixture-vin", slog.New(slog.NewJSONHandler(io.Discard, nil)))
	c.pollInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if got := c.Check(ctx); got.Outcome != Unknown {
		t.Fatalf("result=%+v", got)
	}
}
