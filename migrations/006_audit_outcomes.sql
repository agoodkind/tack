-- Audit outcome and correlation payloads are part of the hash-covered ledger row.
-- Every statement is idempotent because YugabyteDB keeps completed DDL when a
-- migration transaction rolls back.

-- The outcome column allows NULL and does not define a default. New rows
-- include an outcome. Earlier rows retain NULL because their events did not
-- record outcomes and their hashes did not cover outcomes. A default would
-- assert success without evidence and without hash verification.

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
