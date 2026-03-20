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

// WakeAndGetChargingState sends a wake command, polls until the vehicle is
// online, then queries charging state. The caller should set a deadline on ctx
// to bound the total wait time.
func WakeAndGetChargingState(ctx context.Context, client Client, httpClient *http.Client, vin string, pollInterval time.Duration) (string, error) {
	if err := client.WakeUp(ctx, httpClient, vin); err != nil {
		return "", fmt.Errorf("wake vehicle: %w", err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for vehicle online: %w", ctx.Err())
		case <-ticker.C:
			state, err := client.GetVehicleState(ctx, httpClient, vin)
			if err != nil {
				continue // transient error — keep polling
			}
			if state == "online" {
				return client.GetChargingState(ctx, httpClient, vin)
			}
		}
	}
}
