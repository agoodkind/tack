-- The dead-letter table durably stores every audit event the consumer could
-- not commit to audit.events, including payloads it could not decode
-- (TACK-336). A replay re-publishes a row's payload to the audit topic. The
-- consumer, the ledger's only writer, deletes the row in the event's write
-- transaction or counts the attempt if it fails again.
--
-- The table does not expire rows; only a successful replay removes them.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- migration transaction rolls back. A retry must reconcile partial work.

-- +goose Up
ALTER TABLE audit.events_dlq ADD COLUMN IF NOT EXISTS attempt_count   INT         NOT NULL DEFAULT 0;
ALTER TABLE audit.events_dlq ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ;

-- The writer already has a FOR ALL policy on the table, but its grant did
-- not permit the delete that completes a replay. Grant updates only to replay
-- bookkeeping columns; the writer must not change queued payload bytes.
GRANT UPDATE (attempt_count, last_attempt_at, error), DELETE ON audit.events_dlq TO audit_writer;

-- +goose Down
REVOKE UPDATE (attempt_count, last_attempt_at, error), DELETE ON audit.events_dlq FROM audit_writer;
ALTER TABLE audit.events_dlq DROP COLUMN IF EXISTS last_attempt_at;
ALTER TABLE audit.events_dlq DROP COLUMN IF EXISTS attempt_count;
