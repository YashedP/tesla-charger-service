package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed sql/002_charging_runs.sql
var chargingRunsSQL string

const (
	RunChecking = "checking"
	RunPending  = "pending"
	RunHealthy  = "healthy"
	RunAccepted = "accepted"
	RunMissed   = "missed"
	RunFailed   = "failed"
)

type Run struct {
	ID            string
	VehicleID     string
	LocalDate     string
	Timezone      string
	ScheduledAt   int64
	State         string
	Outcome       string
	Reason        string
	CheckAttempts int
	SendAttempts  int
	NextAttemptAt int64
	ObservedAt    int64
	Payload       string
	UpdatedAt     int64
}

const runColumns = `id, vehicle_id, local_date, timezone, scheduled_at_unix,
state, outcome, reason, check_attempts, send_attempts, next_attempt_at_unix,
observed_at_unix, payload, updated_at_unix`

func (s *SQLiteStore) EnsureRun(ctx context.Context, run Run) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO charging_runs
(id, vehicle_id, local_date, timezone, scheduled_at_unix, state, next_attempt_at_unix, updated_at_unix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(vehicle_id, local_date) DO NOTHING`,
		run.ID, run.VehicleID, run.LocalDate, run.Timezone, run.ScheduledAt, run.State, run.NextAttemptAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("ensure charging run: %w", err)
	}
	return nil
}

func (s *SQLiteStore) PendingRuns(ctx context.Context, vehicleID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM charging_runs
WHERE vehicle_id = ? AND state IN ('checking', 'pending') ORDER BY scheduled_at_unix`, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("query pending charging runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var runs []Run
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.VehicleID, &run.LocalDate, &run.Timezone, &run.ScheduledAt, &run.State, &run.Outcome, &run.Reason, &run.CheckAttempts, &run.SendAttempts, &run.NextAttemptAt, &run.ObservedAt, &run.Payload, &run.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan charging run: %w", err)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *SQLiteStore) SaveRun(ctx context.Context, run Run) error {
	result, err := s.db.ExecContext(ctx, `UPDATE charging_runs SET
state = ?, outcome = ?, reason = ?, check_attempts = ?, send_attempts = ?,
next_attempt_at_unix = ?, observed_at_unix = ?, payload = ?, updated_at_unix = ?
WHERE id = ?`, run.State, run.Outcome, run.Reason, run.CheckAttempts, run.SendAttempts, run.NextAttemptAt, run.ObservedAt, run.Payload, run.UpdatedAt, run.ID)
	if err != nil {
		return fmt.Errorf("save charging run: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
