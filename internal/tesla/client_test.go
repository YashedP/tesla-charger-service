package tesla

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetChargingState(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept header = %q, want application/json", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatalf("User-Agent header should be set")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"charge_state":{"charging_state":"Charging"}}}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	state, err := client.GetChargingState(context.Background(), ts.Client(), "5YJ123")
	if err != nil {
		t.Fatalf("get charging state: %v", err)
	}
	if state != "Charging" {
		t.Fatalf("state = %q, want Charging", state)
	}
}

func TestGetChargingStateRetriesTransientError(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"transient gateway failure"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"charge_state":{"charging_state":"Charging"}}}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	state, err := client.GetChargingState(context.Background(), ts.Client(), "5YJ123")
	if err != nil {
		t.Fatalf("get charging state: %v", err)
	}
	if state != "Charging" {
		t.Fatalf("state = %q, want Charging", state)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestGetChargingStateHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	_, err := client.GetChargingState(context.Background(), ts.Client(), "5YJ123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("status=%d", http.StatusBadRequest)) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetChargingStateAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"vehicle_unavailable","error_description":"asleep"}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	_, err := client.GetChargingState(context.Background(), ts.Client(), "5YJ123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tesla error=") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetChargingState408ReturnsErrVehicleUnavailable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
		_, _ = w.Write([]byte(`{"error":"vehicle unavailable"}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	_, err := client.GetChargingState(context.Background(), ts.Client(), "5YJ123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrVehicleUnavailable) {
		t.Fatalf("expected ErrVehicleUnavailable, got: %v", err)
	}
}

func TestWakeUpSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wake_up") {
			t.Fatalf("path = %s, want wake_up endpoint", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"response":{"state":"online"}}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	if err := client.WakeUp(context.Background(), ts.Client(), "5YJ123"); err != nil {
		t.Fatalf("wake up: %v", err)
	}
}

func TestWakeUpError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`service unavailable`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	err := client.WakeUp(context.Background(), ts.Client(), "5YJ123")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("status=%d", http.StatusServiceUnavailable)) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetVehicleStateOnline(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"state":"online"}}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	state, err := client.GetVehicleState(context.Background(), ts.Client(), "5YJ123")
	if err != nil {
		t.Fatalf("get vehicle state: %v", err)
	}
	if state != "online" {
		t.Fatalf("state = %q, want online", state)
	}
}

func TestGetVehicleStateAsleep(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"state":"asleep"}}`))
	}))
	defer ts.Close()

	client := NewFleetClient(ts.URL)
	state, err := client.GetVehicleState(context.Background(), ts.Client(), "5YJ123")
	if err != nil {
		t.Fatalf("get vehicle state: %v", err)
	}
	if state != "asleep" {
		t.Fatalf("state = %q, want asleep", state)
	}
}
