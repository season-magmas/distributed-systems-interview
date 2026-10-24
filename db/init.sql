CREATE DATABASE executor_db;
\c executor_db

CREATE TABLE schema_version (version INTEGER PRIMARY KEY CHECK (version = 1));
INSERT INTO schema_version VALUES (1);

-- The public run and its scheduling projection commit as one aggregate.
CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    record JSONB NOT NULL
);
CREATE TABLE attempts (
    job_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    step_name TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 1 AND 3),
    command JSONB NOT NULL,
    report JSONB,
    UNIQUE (run_id, step_name, ordinal)
);
CREATE TABLE reservations (
    device_id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL UNIQUE REFERENCES attempts(job_id)
);
CREATE TABLE outbox (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    command JSONB NOT NULL,
    published BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE inbox (
    id TEXT PRIMARY KEY,
    payload BYTEA NOT NULL
);
CREATE TABLE rejections (
    id TEXT PRIMARY KEY,
    payload BYTEA NOT NULL,
    reason TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
