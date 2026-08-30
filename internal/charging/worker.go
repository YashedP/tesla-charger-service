package charging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"tesla-charger-service/internal/anchor"
	"tesla-charger-service/internal/schedule"
	"tesla-charger-service/internal/store"
)

const catchUpWindow = 30 * time.Minute
const attemptTimeout = 45 * time.Second

type runStore interface {
	EnsureRun(context.Context, store.Run) error
	PendingRuns(context.Context, string) ([]store.Run, error)
	SaveRun(context.Context, store.Run) error
}
type checker interface{ Check(context.Context) Result }
type notifier interface {
	Send(context.Context, []byte) anchor.SendResult
}
type clock interface {
	Now() time.Time
	WaitUntil(context.Context, time.Time) error
}
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) WaitUntil(ctx context.Context, at time.Time) error {
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type runIDKey struct{}

type Worker struct {
	schedule  schedule.Daily
	vehicleID string
	store     runStore
	checker   checker
	notifier  notifier
	logger    *slog.Logger
	clock     clock
}

func NewWorker(daily schedule.Daily, vin string, runs runStore, check checker, notify notifier, logger *slog.Logger) *Worker {
	hash := sha256.Sum256([]byte(vin))
	return &Worker{schedule: daily, vehicleID: hex.EncodeToString(hash[:]), store: runs, checker: check, notifier: notify, logger: logger, clock: wallClock{}}
}

func (w *Worker) Run(ctx context.Context) error {
	w.logger.InfoContext(ctx, "charging_worker_started", "event", "charging_worker_started", "check_time", w.schedule.Clock, "timezone", w.schedule.Location.String())
	var loggedNext time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := w.step(ctx)
		if err != nil {
			return err
		}
		_, scheduledNext := w.schedule.Around(w.clock.Now())
		if !scheduledNext.Equal(loggedNext) {
			w.logger.InfoContext(ctx, "charging_next_run", "event", "charging_next_run", "next_run", scheduledNext.UTC().Format(time.RFC3339), "timezone", w.schedule.Location.String())
			loggedNext = scheduledNext
		}
		// Re-evaluate wall time periodically to recover from clock corrections.
		// This only checks local state; it does not poll Tesla.
		wake := minTime(next, w.clock.Now().Add(time.Minute))
		if err := w.clock.WaitUntil(ctx, wake); err != nil {
			return err
		}
	}
}

func (w *Worker) step(ctx context.Context) (time.Time, error) {
	now := w.clock.Now()
	previous, next := w.schedule.Around(now)
	date := previous.In(w.schedule.Location).Format("2006-01-02")
	run := store.Run{ID: "tesla-charging:" + w.vehicleID + ":" + date, VehicleID: w.vehicleID, LocalDate: date, Timezone: w.schedule.Location.String(), ScheduledAt: previous.Unix(), State: store.RunChecking, NextAttemptAt: previous.Unix(), UpdatedAt: now.Unix()}
	if err := w.store.EnsureRun(ctx, run); err != nil {
		return next, err
	}
	runs, err := w.store.PendingRuns(ctx, w.vehicleID)
	if err != nil {
		return next, err
	}
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return next, err
		}
		deadline := time.Unix(run.ScheduledAt, 0).Add(catchUpWindow)
		now = w.clock.Now()
		if !now.Before(deadline) {
			run.State = store.RunMissed
			if run.Payload != "" {
				run.State = store.RunFailed
			}
			run.Reason = "catch_up_window_expired"
			if err := w.save(ctx, &run); err != nil {
				return next, err
			}
			w.logRun(ctx, run, run.State)
			continue
		}
		if time.Unix(run.NextAttemptAt, 0).After(now) {
			next = minTime(next, minTime(time.Unix(run.NextAttemptAt, 0), deadline))
			continue
		}
		if run.State == store.RunChecking {
			if err := w.check(ctx, &run, deadline); err != nil {
				return next, err
			}
		}
		if run.State == store.RunPending {
			if err := w.send(ctx, &run, deadline); err != nil {
				return next, err
			}
		}
		if run.State == store.RunChecking || run.State == store.RunPending {
			next = minTime(next, minTime(time.Unix(run.NextAttemptAt, 0), deadline))
		}
	}
	return next, nil
}

func (w *Worker) check(ctx context.Context, run *store.Run, deadline time.Time) error {
	if run.CheckAttempts < 3 {
		run.CheckAttempts++
		// Reserve the attempt durably before contacting Tesla. A crash consumes
		// that attempt and resumes after its timeout rather than looping forever.
		run.NextAttemptAt = retryUnix(w.clock.Now().Add(attemptTimeout + time.Duration(run.CheckAttempts)*15*time.Second))
		if err := w.save(ctx, run); err != nil {
			return err
		}
		w.logRun(ctx, *run, "charging_check_started")
		checkCtx, cancel := context.WithTimeout(context.WithValue(ctx, runIDKey{}, run.ID), min(attemptTimeout, deadline.Sub(w.clock.Now())))
		result := w.checker.Check(checkCtx)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		run.Outcome, run.Reason = result.Outcome, result.Reason
		run.ObservedAt = w.clock.Now().Unix()
	} else {
		run.Outcome, run.Reason = Unknown, "check_interrupted"
		run.ObservedAt = w.clock.Now().Unix()
	}
	switch {
	case run.Outcome == Healthy:
		run.State = store.RunHealthy
	case run.Outcome == Unknown && run.CheckAttempts < 3:
		run.NextAttemptAt = retryUnix(w.clock.Now().Add(time.Duration(run.CheckAttempts) * 15 * time.Second))
	default:
		if err := prepareNotification(run); err != nil {
			return err
		}
		run.State = store.RunPending
		run.NextAttemptAt = w.clock.Now().Unix()
	}
	if err := w.save(ctx, run); err != nil {
		return err
	}
	w.logRun(ctx, *run, "charging_check_result")
	return nil
}

func prepareNotification(run *store.Run) error {
	title := "Tesla is not charging"
	message := "The car was not charging at the nightly check. Please check its charging connection."
	if run.Outcome == Unknown {
		title = "Could not verify Tesla charging"
		message = "The nightly check could not confirm the car's charging state. Please check the Tesla app or car."
	}
	loc, err := time.LoadLocation(run.Timezone)
	if err != nil {
		return fmt.Errorf("load persisted run timezone: %w", err)
	}
	message += " Checked at " + time.Unix(run.ObservedAt, 0).In(loc).Format("2006-01-02 15:04:05 MST") + " (" + run.Timezone + ")."
	payload, err := json.Marshal(anchor.Notification{IdempotencyKey: run.ID, Title: title, Message: message, InitialUrgency: "emergency", MaximumUrgency: "emergency", ResponseRequirement: "acknowledge", MaximumPrompts: 1, DeadlineSeconds: 1800})
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	run.Payload = string(payload)
	return nil
}

func (w *Worker) send(ctx context.Context, run *store.Run, deadline time.Time) error {
	if !w.clock.Now().Before(deadline) {
		return nil
	} // The next step records expiry.
	run.SendAttempts++
	delay := deliveryBackoff(run.SendAttempts)
	run.NextAttemptAt = retryUnix(w.clock.Now().Add(10*time.Second + delay))
	if err := w.save(ctx, run); err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, min(10*time.Second, deadline.Sub(w.clock.Now())))
	result := w.notifier.Send(sendCtx, []byte(run.Payload))
	cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	run.Reason = result.Reason
	switch {
	case result.Accepted:
		run.State = store.RunAccepted
	case !result.Retryable:
		run.State = store.RunFailed
	default:
		retryAt := w.clock.Now().Add(delay)
		if result.RetryAt.After(retryAt) {
			retryAt = result.RetryAt
		}
		run.NextAttemptAt = retryUnix(retryAt)
	}
	if err := w.save(ctx, run); err != nil {
		return err
	}
	w.logRun(ctx, *run, "anchor_"+run.State, "http_status", result.StatusCode)
	return nil
}

func deliveryBackoff(attempt int) time.Duration {
	return min(5*time.Second*time.Duration(1<<min(attempt-1, 6)), 5*time.Minute)
}

func (w *Worker) save(ctx context.Context, run *store.Run) error {
	run.UpdatedAt = w.clock.Now().Unix()
	return w.store.SaveRun(ctx, *run)
}

func (w *Worker) logRun(ctx context.Context, run store.Run, event string, attrs ...any) {
	level := slog.LevelInfo
	if run.State == store.RunMissed || run.State == store.RunFailed {
		level = slog.LevelError
	}
	args := []any{"event", event, "run_id", run.ID, "scheduled_at", time.Unix(run.ScheduledAt, 0).UTC().Format(time.RFC3339), "timezone", run.Timezone, "state", run.State, "outcome", run.Outcome, "reason", run.Reason, "check_attempts", run.CheckAttempts, "send_attempts", run.SendAttempts}
	w.logger.Log(ctx, level, event, append(args, attrs...)...)
}

// Persist whole-second deadlines by rounding up so retries never run earlier
// than a backoff or Retry-After value with fractional seconds.
func retryUnix(at time.Time) int64 {
	seconds := at.Unix()
	if at.Nanosecond() != 0 {
		seconds++
	}
	return seconds
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
