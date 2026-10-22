-- This migration adds storage for pending operator reads and terminal receipts (TACK-583).
--
-- public.ops_read_state stores one row per admitted read. A pending row stores a
-- template and a lease deadline. A terminal row stores the owner, generation,
-- transport digest, outcome, and publication time. The lifecycle functions use
-- database time.
--
-- ops_outbox_owner owns public.ops_read_state and its eight lifecycle functions.
-- ops_read_finalize and ops_read_recover write public.ops_outbox with that owner's
-- privileges. Application roles can execute the functions but cannot access either
-- table directly. tack_migrator temporarily joins ops_outbox_owner. The migration
-- changes each object's owner before revoking PUBLIC access and grants EXECUTE on
-- the functions while tack_migrator remains a member of ops_outbox_owner.
-- The migration then revokes that membership.
--
-- Each migration statement produces the same database state when it runs again.
-- YugabyteDB retains completed changes to database objects when a migration
-- transaction rolls back.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.ops_read_state (
    event_id           UUID        PRIMARY KEY,
    bucket             SMALLINT    NOT NULL,
    owner_id           UUID        NOT NULL,
    generation         BIGINT      NOT NULL,
    admission_deadline TIMESTAMPTZ NOT NULL,
    lease_deadline     TIMESTAMPTZ,
    template           JSONB,
    digest             TEXT,
    outcome            TEXT,
    published_at       TIMESTAMPTZ
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS ops_read_state_pending_idx
    ON public.ops_read_state (bucket, lease_deadline)
    WHERE lease_deadline IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS ops_read_state_receipt_idx
    ON public.ops_read_state (bucket, published_at)
    WHERE published_at IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_begin(
    p_event_id UUID, p_owner UUID, p_bucket SMALLINT, p_template JSONB)
RETURNS TABLE (out_generation BIGINT, out_admission_deadline TIMESTAMPTZ)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    existing public.ops_read_state%ROWTYPE;
BEGIN
    SELECT * INTO existing FROM public.ops_read_state s WHERE s.event_id = p_event_id FOR UPDATE;
    IF NOT FOUND THEN
        INSERT INTO public.ops_read_state
            (event_id, bucket, owner_id, generation, admission_deadline, lease_deadline, template)
        VALUES (p_event_id, p_bucket, p_owner, 1, now() + interval '30 seconds',
                now() + interval '30 seconds', p_template)
        RETURNING generation, admission_deadline INTO out_generation, out_admission_deadline;
        RETURN NEXT;
        RETURN;
    END IF;
    IF existing.template IS NULL OR existing.owner_id <> p_owner OR existing.template <> p_template THEN
        RAISE EXCEPTION 'admission does not own audit read %', p_event_id USING ERRCODE = 'TK001';
    END IF;
    IF now() > existing.admission_deadline THEN
        RAISE EXCEPTION 'audit read % admission retry deadline passed', p_event_id USING ERRCODE = 'TK002';
    END IF;
    out_generation := existing.generation;
    out_admission_deadline := existing.admission_deadline;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_renew(p_event_id UUID, p_owner UUID, p_generation BIGINT)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    UPDATE public.ops_read_state s
       SET lease_deadline = now() + interval '30 seconds'
     WHERE s.event_id = p_event_id AND s.owner_id = p_owner
       AND s.generation = p_generation AND s.template IS NOT NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'token does not own audit read %', p_event_id USING ERRCODE = 'TK001';
    END IF;
END;
$$;
-- +goose StatementEnd

-- ops_read_template returns the template when the owner and generation match a pending read.
-- It returns NULL when a terminal receipt confirms an earlier finalization with the
-- same owner and digest and an outcome other than incomplete.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_template(
    p_event_id UUID, p_owner UUID, p_generation BIGINT, p_digest TEXT)
RETURNS JSONB
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    existing public.ops_read_state%ROWTYPE;
BEGIN
    SELECT * INTO existing FROM public.ops_read_state s WHERE s.event_id = p_event_id;
    IF FOUND AND existing.template IS NOT NULL
       AND existing.owner_id = p_owner AND existing.generation = p_generation THEN
        RETURN existing.template;
    END IF;
    IF FOUND AND existing.template IS NULL AND existing.owner_id = p_owner
       AND existing.digest = p_digest AND existing.outcome <> 'incomplete' THEN
        RETURN NULL;
    END IF;
    RAISE EXCEPTION 'token does not own audit read %', p_event_id USING ERRCODE = 'TK001';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_finalize(
    p_event_id UUID, p_owner UUID, p_generation BIGINT, p_digest TEXT, p_outcome TEXT,
    p_header JSONB, p_parts BYTEA[])
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    existing public.ops_read_state%ROWTYPE;
BEGIN
    SELECT * INTO existing FROM public.ops_read_state s WHERE s.event_id = p_event_id FOR UPDATE;
    IF FOUND AND existing.template IS NULL AND existing.owner_id = p_owner
       AND existing.digest = p_digest AND existing.outcome <> 'incomplete' THEN
        RETURN;
    END IF;
    IF NOT FOUND OR existing.template IS NULL
       OR existing.owner_id <> p_owner OR existing.generation <> p_generation THEN
        RAISE EXCEPTION 'token does not own audit read %', p_event_id USING ERRCODE = 'TK001';
    END IF;
    PERFORM public.ops_outbox_write_encoded(p_event_id, p_header, p_parts);
    UPDATE public.ops_read_state s
       SET template = NULL, lease_deadline = NULL, digest = p_digest, outcome = p_outcome
     WHERE s.event_id = p_event_id;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_expired(p_bucket SMALLINT, p_limit INTEGER)
RETURNS TABLE (out_event_id UUID, out_generation BIGINT, out_lease_deadline TIMESTAMPTZ,
               out_template JSONB, out_observed_at TIMESTAMPTZ)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    SELECT s.event_id, s.generation, s.lease_deadline, s.template, now()
      FROM public.ops_read_state s
     WHERE s.bucket = p_bucket AND s.lease_deadline IS NOT NULL AND s.lease_deadline <= now()
     ORDER BY s.lease_deadline
     LIMIT p_limit;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_recover(
    p_event_id UUID, p_generation BIGINT, p_lease_deadline TIMESTAMPTZ, p_digest TEXT,
    p_header JSONB, p_parts BYTEA[])
RETURNS BOOLEAN
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    UPDATE public.ops_read_state s
       SET generation = s.generation + 1, template = NULL, lease_deadline = NULL,
           digest = p_digest, outcome = 'incomplete'
     WHERE s.event_id = p_event_id AND s.generation = p_generation AND s.template IS NOT NULL
       AND s.lease_deadline = p_lease_deadline AND s.lease_deadline <= now();
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    PERFORM public.ops_outbox_write_encoded(p_event_id, p_header, p_parts);
    RETURN TRUE;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.ops_read_acknowledge(p_event_id UUID, p_digest TEXT)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    UPDATE public.ops_read_state s
       SET published_at = now()
     WHERE s.event_id = p_event_id AND s.template IS NULL AND s.published_at IS NULL
       AND s.digest = p_digest;
    IF NOT FOUND AND EXISTS (
        SELECT 1 FROM public.ops_read_state s
         WHERE s.event_id = p_event_id AND s.template IS NULL AND s.digest <> p_digest) THEN
        RAISE EXCEPTION 'published digest of audit read % differs from its receipt', p_event_id
            USING ERRCODE = 'TK003';
    END IF;
END;
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
DECLARE
    signature TEXT;
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    GRANT CREATE ON SCHEMA public TO ops_outbox_owner;
    ALTER TABLE public.ops_read_state OWNER TO ops_outbox_owner;
    REVOKE ALL ON public.ops_read_state FROM PUBLIC;
    FOREACH signature IN ARRAY ARRAY[
        'public.ops_read_begin(uuid, uuid, smallint, jsonb)',
        'public.ops_read_renew(uuid, uuid, bigint)',
        'public.ops_read_template(uuid, uuid, bigint, text)',
        'public.ops_read_finalize(uuid, uuid, bigint, text, text, jsonb, bytea[])',
        'public.ops_read_expired(smallint, integer)',
        'public.ops_read_recover(uuid, bigint, timestamptz, text, jsonb, bytea[])',
        'public.ops_read_acknowledge(uuid, text)',
        'public.ops_read_cleanup(smallint, integer)'
    ] LOOP
        EXECUTE 'ALTER FUNCTION ' || signature || ' OWNER TO ops_outbox_owner';
        EXECUTE 'REVOKE ALL ON FUNCTION ' || signature || ' FROM PUBLIC';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ' || signature || ' TO app_auth, tack_migrator';
    END LOOP;
    -- The relay records a receipt's publication time and deletes the delivered
    -- outbox row in one transaction.
    GRANT EXECUTE ON FUNCTION public.ops_read_acknowledge(uuid, text) TO audit_writer;
    REVOKE CREATE ON SCHEMA public FROM ops_outbox_owner;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS public.ops_read_cleanup(smallint, integer);
DROP FUNCTION IF EXISTS public.ops_read_acknowledge(uuid, text);
DROP FUNCTION IF EXISTS public.ops_read_recover(uuid, bigint, timestamptz, text, jsonb, bytea[]);
DROP FUNCTION IF EXISTS public.ops_read_expired(smallint, integer);
DROP FUNCTION IF EXISTS public.ops_read_finalize(uuid, uuid, bigint, text, text, jsonb, bytea[]);
DROP FUNCTION IF EXISTS public.ops_read_template(uuid, uuid, bigint, text);
DROP FUNCTION IF EXISTS public.ops_read_renew(uuid, uuid, bigint);
DROP FUNCTION IF EXISTS public.ops_read_begin(uuid, uuid, smallint, jsonb);
DROP TABLE IF EXISTS public.ops_read_state;
-- +goose StatementEnd
