package charging

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"tesla-charger-service/internal/tesla"
)

const (
	Healthy     = "healthy"
	NotCharging = "not_charging"
	Unknown     = "unknown"
)

type Result struct {
	Outcome string
	Reason  string
}

type tokenSource interface {
	Fresh(context.Context) (*oauth2.Token, error)
}

type Checker struct {
	tokens       tokenSource
	fleet        tesla.Client
	vin          string
	logger       *slog.Logger
	pollInterval time.Duration
}

func NewChecker(tokens tokenSource, fleet tesla.Client, vin string, logger *slog.Logger) *Checker {
	return &Checker{tokens: tokens, fleet: fleet, vin: vin, logger: logger, pollInterval: 2 * time.Second}
}

// Check performs a single bounded observation. Unknown is never represented as
// either charging or not charging; the worker owns retries and alert policy.
func (c *Checker) Check(ctx context.Context) Result {
	tok, err := c.tokens.Fresh(ctx)
	if err != nil {
		return Result{Outcome: Unknown, Reason: "token_unavailable"}
	}
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(tok))
	state, err := c.fleet.GetChargingState(ctx, client, c.vin)
	if errors.Is(err, tesla.ErrVehicleUnavailable) {
		state, err = tesla.WakeAndGetChargingStateWithObserver(ctx, c.fleet, client, c.vin, c.pollInterval, func(event tesla.WakeEvent) {
			// Never log upstream error strings, URLs, VINs, or response bodies.
			c.logger.InfoContext(ctx, event.Event, "event", event.Event, "run_id", ctx.Value(runIDKey{}), "attempt", event.Attempt, "failed", event.Err != nil)
		})
	}
	if err != nil {
		return Result{Outcome: Unknown, Reason: "vehicle_unavailable"}
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "charging", "complete":
		return Result{Outcome: Healthy, Reason: strings.ToLower(state)}
	case "stopped", "disconnected", "nopower":
		return Result{Outcome: NotCharging, Reason: strings.ToLower(state)}
	default:
		return Result{Outcome: Unknown, Reason: "unrecognized_charge_state"}
	}
}
