-- +goose Up
-- River job queue schema — present from day 1, no jobs yet.
-- When background work is needed (emails, webhooks, search rebuild), river slots in
-- with zero schema changes.
CREATE TABLE river_jobs (
    id           BIGSERIAL PRIMARY KEY,
    state        TEXT NOT NULL DEFAULT 'available'
                     CHECK(state IN ('available','cancelled','completed','discarded',
                                     'pending','retryable','running','scheduled')),
    attempt      SMALLINT NOT NULL DEFAULT 0,
    max_attempts SMALLINT NOT NULL,
    attempted_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finalized_at TIMESTAMPTZ,
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    priority     SMALLINT NOT NULL DEFAULT 1,
    args         JSONB NOT NULL DEFAULT '{}',
    errors       JSONB,
    kind         TEXT NOT NULL,
    queue        TEXT NOT NULL DEFAULT 'default',
    tags         TEXT[] NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_river_jobs_kind_state ON river_jobs(kind, state);
CREATE INDEX idx_river_jobs_scheduled  ON river_jobs(scheduled_at, priority)
    WHERE state IN ('available','scheduled','retryable');

-- +goose Down
DROP INDEX idx_river_jobs_scheduled;
DROP INDEX idx_river_jobs_kind_state;
DROP TABLE river_jobs;
