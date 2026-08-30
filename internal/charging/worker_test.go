package charging

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tesla-charger-service/internal/anchor"
	"tesla-charger-service/internal/crypto"
	"tesla-charger-service/internal/schedule"
	"tesla-charger-service/internal/store"
)

type fakeClock struct{ now, stopAt time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) WaitUntil(ctx context.Context, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.stopAt.IsZero() && !at.Before(c.stopAt) {
		return context.Canceled
	}
	c.now = at
	return nil
}

type fakeChecker struct {
	result Result
	calls  int
}

func (c *fakeChecker) Check(context.Context) Result { c.calls++; return c.result }

type fakeNotifier struct {
	result   anchor.SendResult
	payloads []string
}

func (n *fakeNotifier) Send(_ context.Context, payload []byte) anchor.SendResult {
	n.payloads = append(n.payloads, string(payload))
	return n.result
}

func newTestStore(t *testing.T, path string) *store.SQLiteStore {
	t.Helper()
	cipher, err := crypto.NewAESCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSQLiteStore(path, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testWorker(t *testing.T, path string, clock *fakeClock, check checker, notify notifier) *Worker {
	t.Helper()
	daily, err := schedule.Parse("23:00", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker(daily, "fixture-vin", newTestStore(t, path), check, notify, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	w.clock = clock
	return w
}

func scheduledInstant() time.Time { return time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC) }

func readRun(t *testing.T, path string) store.Run {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var run store.Run
	err = db.QueryRow(`SELECT id, state, outcome, reason, check_attempts, send_attempts, next_attempt_at_unix, payload FROM charging_runs WHERE local_date = '2026-08-30'`).Scan(&run.ID, &run.State, &run.Outcome, &run.Reason, &run.CheckAttempts, &run.SendAttempts, &run.NextAttemptAt, &run.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func step(t *testing.T, w *Worker) time.Time {
	t.Helper()
	next, err := w.step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestOutcomesAndDuplicateSuppression(t *testing.T) {
	for _, outcome := range []string{Healthy, NotCharging, Unknown} {
		t.Run(outcome, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.sqlite")
			clock := &fakeClock{now: scheduledInstant()}
			check := &fakeChecker{result: Result{Outcome: outcome, Reason: "fixture"}}
			notify := &fakeNotifier{result: anchor.SendResult{Accepted: true, Reason: "accepted"}}
			w := testWorker(t, path, clock, check, notify)
			next := step(t, w)
			if outcome == Unknown {
				if next.Sub(clock.now) != 15*time.Second {
					t.Fatal("first backoff incorrect")
				}
				clock.now = next
				next = step(t, w)
				if next.Sub(clock.now) != 30*time.Second {
					t.Fatal("second backoff incorrect")
				}
				clock.now = next
				step(t, w)
			}
			run := readRun(t, path)
			if outcome == Healthy {
				if run.State != store.RunHealthy || len(notify.payloads) != 0 {
					t.Fatal("healthy check alerted")
				}
			} else {
				if run.State != store.RunAccepted || len(notify.payloads) != 1 {
					t.Fatalf("run=%+v sends=%d", run, len(notify.payloads))
				}
				var payload anchor.Notification
				if err := json.Unmarshal([]byte(notify.payloads[0]), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.InitialUrgency != "emergency" || payload.MaximumUrgency != "emergency" || payload.ResponseRequirement != "acknowledge" || payload.MaximumPrompts != 1 || payload.DeadlineSeconds != 1800 || payload.SkipEnabled || payload.SnoozeEnabled {
					t.Fatalf("incorrect alarm policy: %+v", payload)
				}
				if !strings.Contains(payload.Message, "America/New_York") || strings.Contains(notify.payloads[0], "fixture-vin") {
					t.Fatal("missing timestamp or exposed VIN")
				}
				if (payload.Title == "Could not verify Tesla charging") != (outcome == Unknown) {
					t.Fatal("unknown mislabeled as non-charging")
				}
			}
			calls := check.calls
			// A restart and even a same-day schedule change do not create a second run.
			restarted := testWorker(t, path, clock, check, notify)
			restarted.schedule, _ = schedule.Parse("22:59", "America/New_York")
			step(t, restarted)
			if check.calls != calls || len(notify.payloads) > 1 {
				t.Fatal("duplicate after restart")
			}
		})
	}
}

func TestCatchUpAndExpiry(t *testing.T) {
	for _, age := range []time.Duration{20 * time.Minute, 30 * time.Minute, 2 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.sqlite")
			check := &fakeChecker{result: Result{Outcome: NotCharging}}
			notify := &fakeNotifier{result: anchor.SendResult{Accepted: true}}
			w := testWorker(t, path, &fakeClock{now: scheduledInstant().Add(age)}, check, notify)
			step(t, w)
			run := readRun(t, path)
			if age < catchUpWindow {
				if check.calls != 1 || run.State != store.RunAccepted {
					t.Fatal("missed valid catch-up")
				}
			} else if check.calls != 0 || len(notify.payloads) != 0 || run.State != store.RunMissed {
				t.Fatal("sent stale alarm")
			}
		})
	}
}

func TestRetryPersistenceAndTerminalFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	clock := &fakeClock{now: scheduledInstant()}
	check := &fakeChecker{result: Result{Outcome: NotCharging}}
	notify := &fakeNotifier{result: anchor.SendResult{Retryable: true, RetryAt: clock.now.Add(2 * time.Minute), Reason: "rate_limited"}}
	w := testWorker(t, path, clock, check, notify)
	next := step(t, w)
	if !next.Equal(clock.now.Add(2 * time.Minute)) {
		t.Fatal("Retry-After not honored")
	}
	clock.now = clock.now.Add(time.Minute)
	restarted := testWorker(t, path, clock, check, notify)
	step(t, restarted)
	if len(notify.payloads) != 1 {
		t.Fatal("retried before persisted backoff")
	}
	clock.now = next
	notify.result = anchor.SendResult{Reason: "rejected", StatusCode: 409}
	step(t, restarted)
	if len(notify.payloads) != 2 || notify.payloads[0] != notify.payloads[1] || check.calls != 1 {
		t.Fatal("payload changed or Tesla rechecked on retry")
	}
	if readRun(t, path).State != store.RunFailed {
		t.Fatal("permanent rejection not terminal")
	}
	step(t, restarted)
	if len(notify.payloads) != 2 {
		t.Fatal("retried permanent failure")
	}
}

func TestPendingDeliveryExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	clock := &fakeClock{now: scheduledInstant()}
	check := &fakeChecker{result: Result{Outcome: NotCharging}}
	notify := &fakeNotifier{result: anchor.SendResult{Retryable: true, RetryAt: clock.now.Add(time.Hour)}}
	w := testWorker(t, path, clock, check, notify)
	next := step(t, w)
	if !next.Equal(clock.now.Add(catchUpWindow)) {
		t.Fatal("expiry not scheduled")
	}
	clock.now = next
	step(t, w)
	if readRun(t, path).State != store.RunFailed || len(notify.payloads) != 1 {
		t.Fatal("expired delivery retried")
	}
}

type interruptedStore struct {
	runStore
	failState string
	failed    bool
}

func (s *interruptedStore) SaveRun(ctx context.Context, run store.Run) error {
	if run.State == s.failState && !s.failed {
		s.failed = true
		return errors.New("simulated process interruption")
	}
	return s.runStore.SaveRun(ctx, run)
}

func TestCrashBeforeAndAfterSend(t *testing.T) {
	for _, failState := range []string{store.RunPending, store.RunAccepted} {
		t.Run(failState, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.sqlite")
			clock := &fakeClock{now: scheduledInstant()}
			check := &fakeChecker{result: Result{Outcome: NotCharging}}
			notify := &fakeNotifier{result: anchor.SendResult{Accepted: true}}
			w := testWorker(t, path, clock, check, notify)
			w.store = &interruptedStore{runStore: w.store, failState: failState}
			if _, err := w.step(context.Background()); err == nil {
				t.Fatal("interruption not propagated")
			}
			clock.now = clock.now.Add(2 * time.Minute)
			restarted := testWorker(t, path, clock, check, notify)
			step(t, restarted)
			if readRun(t, path).State != store.RunAccepted {
				t.Fatal("run did not recover")
			}
			if failState == store.RunAccepted {
				if len(notify.payloads) != 2 || notify.payloads[0] != notify.payloads[1] || check.calls != 1 {
					t.Fatal("ambiguous delivery was not replayed identically")
				}
			} else if len(notify.payloads) != 1 {
				t.Fatal("sent before saving payload")
			}
		})
	}
}

func TestCanceledWorkerDoesNotSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	check := &fakeChecker{result: Result{Outcome: NotCharging}}
	notify := &fakeNotifier{}
	w := testWorker(t, path, &fakeClock{now: scheduledInstant()}, check, notify)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if check.calls != 0 || len(notify.payloads) != 0 {
		t.Fatal("canceled worker performed work")
	}
}

type checkerFunc func(context.Context) Result

func (f checkerFunc) Check(ctx context.Context) Result { return f(ctx) }

func TestAttemptDeadlineAndInFlightCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	clock := &fakeClock{now: scheduledInstant().Add(29*time.Minute + 50*time.Second)}
	notify := &fakeNotifier{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	check := checkerFunc(func(attempt context.Context) Result {
		deadline, ok := attempt.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Error("attempt exceeds remaining catch-up window")
		}
		cancel()
		return Result{Outcome: Unknown}
	})
	w := testWorker(t, path, clock, check, notify)
	if _, err := w.step(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if len(notify.payloads) != 0 || readRun(t, path).State != store.RunChecking {
		t.Fatal("shutdown converted to emergency alert")
	}
}

func TestInterruptedLastCheckBecomesUnknownAlert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	clock := &fakeClock{now: scheduledInstant().Add(3 * time.Minute)}
	check := &fakeChecker{result: Result{Outcome: Healthy}}
	notify := &fakeNotifier{result: anchor.SendResult{Accepted: true}}
	w := testWorker(t, path, clock, check, notify)
	run := store.Run{ID: "interrupted-run", VehicleID: w.vehicleID, LocalDate: "2026-08-30", Timezone: "America/New_York", State: store.RunChecking, ScheduledAt: scheduledInstant().Unix(), CheckAttempts: 3, NextAttemptAt: scheduledInstant().Unix()}
	if err := w.store.EnsureRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := w.store.SaveRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	step(t, w)
	if check.calls != 0 || len(notify.payloads) != 1 || readRun(t, path).Outcome != Unknown {
		t.Fatal("interrupted check retried indefinitely or misclassified")
	}
}

func TestCatchUpAcrossMidnight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	clock := &fakeClock{now: scheduledInstant().Add(65 * time.Minute)} // 00:05 local the next day.
	check := &fakeChecker{result: Result{Outcome: NotCharging}}
	notify := &fakeNotifier{result: anchor.SendResult{Accepted: true}}
	w := testWorker(t, path, clock, check, notify)
	var err error
	w.schedule, err = schedule.Parse("23:55", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	step(t, w)
	if readRun(t, path).State != store.RunAccepted || len(notify.payloads) != 1 {
		t.Fatal("lost previous calendar date's catch-up")
	}
}

func TestFractionalRetryAfterIsNotRoundedDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	clock := &fakeClock{now: scheduledInstant()}
	retryAt := clock.now.Add(60*time.Second + 250*time.Millisecond)
	notify := &fakeNotifier{result: anchor.SendResult{Retryable: true, RetryAt: retryAt}}
	w := testWorker(t, path, clock, &fakeChecker{result: Result{Outcome: NotCharging}}, notify)
	next := step(t, w)
	if next.Before(retryAt) {
		t.Fatal("retried earlier than Retry-After")
	}
	clock.now = retryAt.Add(-time.Millisecond)
	step(t, w)
	if len(notify.payloads) != 1 {
		t.Fatal("early retry")
	}
}
