-- public.ops_read_cleanup locks the published receipts it deletes with
-- FOR UPDATE SKIP LOCKED (TACK-583).
--
-- Every application instance runs ops_read_cleanup for every bucket without a
-- shared lock. Two concurrent calls for one bucket selected the same receipts,
-- and their deletes waited on each other's row and ops_read_state_receipt_idx
-- writes until YugabyteDB aborted one call with a deadlock. With SKIP LOCKED,
-- a call does not wait for a receipt that another call has locked and deletes
-- the other receipts that are due.
--
-- CREATE OR REPLACE keeps the function owner, ops_outbox_owner, and its
-- EXECUTE grants. The current database user joins ops_outbox_owner to replace
-- the function and leaves it afterward. Each statement produces the same
-- database state when it runs again.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_cleanup(p_bucket SMALLINT, p_limit INTEGER)
RETURNS INTEGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    due UUID[] := '{}';
    candidate UUID;
    deleted INTEGER;
BEGIN
    FOR candidate IN
        SELECT s.event_id FROM public.ops_read_state s
         WHERE s.bucket = p_bucket AND s.published_at IS NOT NULL
           AND s.published_at <= now() - interval '1 hour'
         ORDER BY s.published_at
         LIMIT p_limit
           FOR UPDATE SKIP LOCKED
    LOOP
        due := due || candidate;
    END LOOP;
    DELETE FROM public.ops_read_state s WHERE s.event_id = ANY(due);
    GET DIAGNOSTICS deleted = ROW_COUNT;
    RETURN deleted;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_cleanup(p_bucket SMALLINT, p_limit INTEGER)
RETURNS INTEGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    deleted INTEGER;
BEGIN
    DELETE FROM public.ops_read_state s
     WHERE s.event_id IN (
        SELECT due.event_id FROM public.ops_read_state due
         WHERE due.bucket = p_bucket AND due.published_at IS NOT NULL
           AND due.published_at <= now() - interval '1 hour'
         ORDER BY due.published_at
         LIMIT p_limit);
    GET DIAGNOSTICS deleted = ROW_COUNT;
    RETURN deleted;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd
