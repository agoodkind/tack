-- Audit outcome and correlation payloads are part of the hash-covered ledger row.
-- Every statement is idempotent because YugabyteDB keeps completed DDL when a
-- migration transaction rolls back.

-- The outcome column is deliberately nullable with no default. Every row
-- written from now on carries a value, but a row that predates this migration
-- never had its outcome recorded, and a default would assert success for an
-- event nobody observed succeeding. Those rows read back as unrecorded, which
-- is the truth. Their hash also predates outcome coverage, so a stored default
-- would be an unverifiable claim sitting in a tamper-evident table.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE audit.events
    ADD COLUMN IF NOT EXISTS outcome TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE audit.events
    ADD COLUMN IF NOT EXISTS error JSONB;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE audit.events
    ADD COLUMN IF NOT EXISTS extra JSONB;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE audit.events
    ADD COLUMN IF NOT EXISTS hash_version SMALLINT NOT NULL DEFAULT 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE audit.events DROP COLUMN IF EXISTS hash_version;
ALTER TABLE audit.events DROP COLUMN IF EXISTS extra;
ALTER TABLE audit.events DROP COLUMN IF EXISTS error;
ALTER TABLE audit.events DROP COLUMN IF EXISTS outcome;
-- +goose StatementEnd
