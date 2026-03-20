package tesla

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// mockClient implements Client for testing WakeAndGetChargingState.
type mockClient struct {
	wakeErr       error
	states        []string // returned in order by GetVehicleState
	stateIdx      int32
	chargingState string
	chargingErr   error
}

func (m *mockClient) GetChargingState(_ context.Context, _ *http.Client, _ string) (string, error) {
	return m.chargingState, m.chargingErr
}

func (m *mockClient) WakeUp(_ context.Context, _ *http.Client, _ string) error {
	return m.wakeErr
}

func (m *mockClient) GetVehicleState(_ context.Context, _ *http.Client, _ string) (string, error) {
	idx := atomic.AddInt32(&m.stateIdx, 1) - 1
	if int(idx) >= len(m.states) {
		return m.states[len(m.states)-1], nil
	}
	return m.states[idx], nil
}

func TestWakeAndGetChargingStateSuccess(t *testing.T) {
	mc := &mockClient{
		states:        []string{"asleep", "online"},
		chargingState: "Charging",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	state, err := WakeAndGetChargingState(ctx, mc, http.DefaultClient, "5YJ123", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "Charging" {
		t.Fatalf("state = %q, want Charging", state)
	}
}

func TestWakeAndGetChargingStateWakeFails(t *testing.T) {
	mc := &mockClient{
		wakeErr: fmt.Errorf("connection refused"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := WakeAndGetChargingState(ctx, mc, http.DefaultClient, "5YJ123", 10*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestWakeAndGetChargingStateTimeout(t *testing.T) {
	mc := &mockClient{
		states: []string{"asleep"}, // never comes online
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := WakeAndGetChargingState(ctx, mc, http.DefaultClient, "5YJ123", 10*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}
