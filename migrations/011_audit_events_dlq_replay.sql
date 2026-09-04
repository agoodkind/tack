-- The dead-letter table becomes the durable landing zone for every audit
-- event the consumer could not commit to audit.events, not only for payloads
-- it could not decode (TACK-336). A replay re-publishes a row's payload to the
-- audit topic and the consumer, still the only writer of the ledger, deletes
-- the row when the event lands or counts the attempt when it fails again.
--
-- The table has no time-based expiry and gains none here; a row leaves only
-- through a successful replay.
--
-- Every statement is idempotent. YugabyteDB keeps completed DDL when a
-- migration transaction rolls back, so a retry must reconcile partial work.

-- +goose Up
ALTER TABLE audit.events_dlq ADD COLUMN IF NOT EXISTS attempt_count   INT         NOT NULL DEFAULT 0;
ALTER TABLE audit.events_dlq ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ;

-- The writer already holds a FOR ALL policy on the table; the grant was the
-- narrower of the two and never allowed the delete a replay ends with. The
-- update is scoped to the replay bookkeeping so the queued payload, the
-- bytes a replay must reproduce, stays beyond the writer's reach.
GRANT UPDATE (attempt_count, last_attempt_at, error), DELETE ON audit.events_dlq TO audit_writer;

-- +goose Down
REVOKE UPDATE (attempt_count, last_attempt_at, error), DELETE ON audit.events_dlq FROM audit_writer;
ALTER TABLE audit.events_dlq DROP COLUMN IF EXISTS last_attempt_at;
ALTER TABLE audit.events_dlq DROP COLUMN IF EXISTS attempt_count;
