package tesla

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Client defines the Tesla API methods needed by the wake orchestration.
type Client interface {
	GetChargingState(ctx context.Context, httpClient *http.Client, vin string) (string, error)
	WakeUp(ctx context.Context, httpClient *http.Client, vin string) error
	GetVehicleState(ctx context.Context, httpClient *http.Client, vin string) (string, error)
}

type WakeEvent struct {
	Event   string
	Attempt int
	State   string
	Err     error
}

type WakeObserver func(WakeEvent)

// WakeAndGetChargingStateWithObserver sends a wake command, polls until the vehicle is
// online, then queries charging state. The caller should set a deadline on ctx
// to bound the total wait time.
func WakeAndGetChargingStateWithObserver(ctx context.Context, client Client, httpClient *http.Client, vin string, pollInterval time.Duration, observer WakeObserver) (string, error) {
	observe := func(event WakeEvent) {
		if observer != nil {
			observer(event)
		}
	}

	observe(WakeEvent{Event: "vehicle_wake_command_start"})
	if err := client.WakeUp(ctx, httpClient, vin); err != nil {
		observe(WakeEvent{Event: "vehicle_wake_command_failed", Err: err})
		return "", fmt.Errorf("wake vehicle: %w", err)
	}
	observe(WakeEvent{Event: "vehicle_wake_command_sent"})

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	attempt := 0
	for {
		select {
		case <-ctx.Done():
			observe(WakeEvent{Event: "vehicle_wake_timeout", Attempt: attempt, Err: ctx.Err()})
			return "", fmt.Errorf("waiting for vehicle online: %w", ctx.Err())
		case <-ticker.C:
			attempt++
			state, err := client.GetVehicleState(ctx, httpClient, vin)
			if err != nil {
				observe(WakeEvent{Event: "vehicle_wake_poll_failed", Attempt: attempt, Err: err})
				continue // transient error — keep polling
			}
			observe(WakeEvent{Event: "vehicle_wake_poll", Attempt: attempt, State: state})
			if state == "online" {
				observe(WakeEvent{Event: "vehicle_online", Attempt: attempt})
				return client.GetChargingState(ctx, httpClient, vin)
			}
		}
	}
}
