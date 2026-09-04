-- Every notarization names the host that signed it (TACK-437). The signature
-- proves the key; the host column makes a row claim an origin, so a forged
-- row from a leaked key has to name a host and attribution no longer rests on
-- the key alone. Rows written before this carry an empty host.
--
-- Idempotent: YugabyteDB keeps completed DDL when a migration transaction
-- rolls back, so a retry must reconcile partial work.

-- +goose Up
ALTER TABLE audit.notarizations ADD COLUMN IF NOT EXISTS signing_host TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit.notarizations DROP COLUMN IF EXISTS signing_host;
