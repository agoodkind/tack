-- Every notarization records its claimed signing host (TACK-437). The
-- signature covers only the Merkle root, not the host. Someone with a leaked
-- key can claim any host. The signer report lists the hosts a rejected key
-- claimed. Earlier rows use an empty host.
--
-- Idempotent: YugabyteDB keeps completed DDL when a migration transaction
-- rolls back, so a retry must reconcile partial work.

-- +goose Up
ALTER TABLE audit.notarizations ADD COLUMN IF NOT EXISTS signing_host TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit.notarizations DROP COLUMN IF EXISTS signing_host;
