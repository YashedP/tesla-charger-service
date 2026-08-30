package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"tesla-charger-service/internal/crypto"
)

func TestUpgradePreservesEncryptedTokensAndRunProgress(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tesla.sqlite")
	cipher, err := crypto.NewAESCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	// Build the immediately previous deployed schema, with real encrypted data.
	db, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, migrateSQL); err != nil {
		t.Fatal(err)
	}
	old := &SQLiteStore{db: db, cipher: cipher}
	token := &oauth2.Token{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", TokenType: "Bearer", Expiry: time.Unix(2000000000, 0)}
	if err := old.SaveToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewSQLiteStore(path, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgraded.Close() }()
	got, err := upgraded.LoadToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != token.AccessToken || got.RefreshToken != token.RefreshToken || !got.Expiry.Equal(token.Expiry) {
		t.Fatal("migration changed tokens")
	}
	var encrypted string
	if err := upgraded.db.QueryRowContext(ctx, "SELECT access_token FROM oauth_tokens").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == token.AccessToken {
		t.Fatal("token stored without encryption")
	}
	run := Run{ID: "run-1", VehicleID: "hash", LocalDate: "2026-08-30", Timezone: "America/New_York", State: RunChecking, ScheduledAt: 123, NextAttemptAt: 123, UpdatedAt: 123}
	if err := upgraded.EnsureRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.State, run.Payload, run.CheckAttempts, run.NextAttemptAt = RunPending, `{"idempotency_key":"run-1"}`, 1, 200
	if err := upgraded.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// Repeated startup and a changed schedule cannot overwrite today's payload.
	run.ID, run.ScheduledAt, run.State = "another-id", 999, RunChecking
	if err := upgraded.EnsureRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := upgraded.PendingRuns(ctx, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "run-1" || pending[0].State != RunPending || pending[0].ScheduledAt != 123 || pending[0].NextAttemptAt != 200 || pending[0].Payload != run.Payload {
		t.Fatalf("run changed: %+v", pending)
	}
	other, err := upgraded.PendingRuns(ctx, "other-vehicle")
	if err != nil || len(other) != 0 {
		t.Fatal("mixed vehicle runs")
	}
}
