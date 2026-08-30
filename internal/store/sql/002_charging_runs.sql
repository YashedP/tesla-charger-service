CREATE TABLE IF NOT EXISTS charging_runs (
    id TEXT PRIMARY KEY,
    vehicle_id TEXT NOT NULL,
    local_date TEXT NOT NULL,
    timezone TEXT NOT NULL,
    scheduled_at_unix INTEGER NOT NULL,
    state TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    check_attempts INTEGER NOT NULL DEFAULT 0,
    send_attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at_unix INTEGER NOT NULL,
    observed_at_unix INTEGER NOT NULL DEFAULT 0,
    payload TEXT NOT NULL DEFAULT '',
    updated_at_unix INTEGER NOT NULL,
    UNIQUE (vehicle_id, local_date)
);
CREATE INDEX IF NOT EXISTS charging_runs_pending
    ON charging_runs (vehicle_id, state, scheduled_at_unix);
