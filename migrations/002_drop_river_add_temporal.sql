-- +goose Up
DROP TABLE IF EXISTS river_jobs;
DROP INDEX IF EXISTS idx_river_jobs_kind_state;
DROP INDEX IF EXISTS idx_river_jobs_scheduled;

-- +goose Down
CREATE TABLE river_jobs (
    id           bigserial PRIMARY KEY,
    state        text      NOT NULL DEFAULT 'available',
    attempt      smallint  NOT NULL DEFAULT 0,
    max_attempts smallint  NOT NULL,
    attempted_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finalized_at timestamptz,
    scheduled_at timestamptz NOT NULL DEFAULT now(),
    priority     smallint  NOT NULL DEFAULT 1,
    args         jsonb     NOT NULL DEFAULT '{}',
    errors       jsonb,
    kind         text      NOT NULL,
    queue        text      NOT NULL DEFAULT 'default',
    tags         text[]    NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_river_jobs_kind_state ON river_jobs(kind, state);
CREATE INDEX idx_river_jobs_scheduled  ON river_jobs(scheduled_at, priority)
    WHERE state IN ('available','scheduled','retryable');
