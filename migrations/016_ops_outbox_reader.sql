-- `ops audit consumer-offsets` reports how many operator-command events wait
-- in public.ops_outbox and when the oldest one was written. It reads through
-- the ledger reader. The ledger reader does not have the relay's writer
-- credential. The reader gains SELECT on the two columns that count and age
-- need, and no access to the event payloads.
--
-- Every statement is idempotent. A retry after a partial run applies the same
-- grant again.

-- +goose Up
GRANT SELECT (event_id, created_at) ON public.ops_outbox TO audit_reader;

-- +goose Down
REVOKE SELECT (event_id, created_at) ON public.ops_outbox FROM audit_reader;
