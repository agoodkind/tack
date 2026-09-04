-- Every notarization names the host that signed it (TACK-437). The host is a
-- claim written beside the signature, not inside it: the signature covers the
-- Merkle root alone, so a holder of a leaked key can claim any host. What the
-- column adds is that every row now states an origin, so the signer report can
-- name which hosts a rejected key claimed to be. Rows written before this
-- carry an empty host.
--
-- Idempotent: YugabyteDB keeps completed DDL when a migration transaction
-- rolls back, so a retry must reconcile partial work.

-- +goose Up
ALTER TABLE audit.notarizations ADD COLUMN IF NOT EXISTS signing_host TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit.notarizations DROP COLUMN IF EXISTS signing_host;
