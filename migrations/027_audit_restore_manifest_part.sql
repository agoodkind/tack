-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.restore_missing_manifest_part(
    p_org UUID, p_event UUID, p_source UUID, p_part_index BIGINT, p_root TEXT,
    p_row_hash BYTEA, p_seq BIGINT, p_shard SMALLINT,
    p_source_row_hash BYTEA, p_source_seq BIGINT, p_source_shard SMALLINT,
    p_change_event_id UUID, p_change_header JSONB, p_change_parts BYTEA[])
RETURNS BIGINT
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, audit, public
AS $$
DECLARE
    matched BIGINT;
    existing BIGINT;
    source_digest TEXT;
    source_payload BYTEA;
    inserted BIGINT;
BEGIN
    IF p_part_index <> 0 THEN
        RAISE EXCEPTION 'restore refused: part index % is not 0', p_part_index USING ERRCODE = 'TK620';
    END IF;
    IF p_source = p_event THEN
        RAISE EXCEPTION 'restore refused: the source event is the target event' USING ERRCODE = 'TK620';
    END IF;

    SELECT count(*) INTO matched FROM audit.events e
     WHERE e.org_id = p_org AND e.event_id = p_event AND e.shard = p_shard AND e.seq = p_seq
       AND e.row_hash = p_row_hash
       AND e.context #>> '{read,manifest_sha256}' = p_root
       AND e.context #>> '{read,manifest_parts}' = '1';
    IF matched <> 1 THEN
        RAISE EXCEPTION 'restore refused: the target event does not match the pinned org, row hash, seq, shard, and one-part root'
            USING ERRCODE = 'TK620';
    END IF;

    SELECT count(*) INTO existing FROM audit.event_parts p
     WHERE p.org_id = p_org AND p.event_id = p_event;
    IF existing <> 0 THEN
        RAISE EXCEPTION 'restore refused: the target event already has % part rows', existing USING ERRCODE = 'TK620';
    END IF;

    SELECT count(*) INTO matched FROM audit.events e
     WHERE e.org_id = p_org AND e.event_id = p_source AND e.shard = p_source_shard AND e.seq = p_source_seq
       AND e.row_hash = p_source_row_hash
       AND e.context #>> '{read,manifest_sha256}' = p_root
       AND e.context #>> '{read,manifest_parts}' = '1';
    IF matched <> 1 THEN
        RAISE EXCEPTION 'restore refused: the source event does not match the pinned org, row hash, seq, shard, and one-part root'
            USING ERRCODE = 'TK620';
    END IF;

    SELECT p.digest, p.payload INTO source_digest, source_payload FROM audit.event_parts p
     WHERE p.org_id = p_org AND p.event_id = p_source AND p.part_index = 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'restore refused: the source event does not store part 0' USING ERRCODE = 'TK620';
    END IF;
    IF source_digest <> p_root OR encode(sha256(source_payload), 'hex') <> p_root THEN
        RAISE EXCEPTION 'restore refused: the source part does not hash to the declared root' USING ERRCODE = 'TK620';
    END IF;

    INSERT INTO audit.event_parts (org_id, event_id, part_index, digest, payload)
    VALUES (p_org, p_event, 0, source_digest, source_payload);
    GET DIAGNOSTICS inserted = ROW_COUNT;
    IF inserted <> 1 THEN
        RAISE EXCEPTION 'restore refused: inserted % part rows, want 1', inserted USING ERRCODE = 'TK620';
    END IF;

    PERFORM public.ops_outbox_write_encoded(p_change_event_id, p_change_header, p_change_parts);
    RETURN inserted;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT audit_writer TO ' || quote_ident(current_user);
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    GRANT CREATE ON SCHEMA public TO audit_writer;
    ALTER FUNCTION public.restore_missing_manifest_part(
        UUID, UUID, UUID, BIGINT, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[])
        OWNER TO audit_writer;
    REVOKE ALL ON FUNCTION public.restore_missing_manifest_part(
        UUID, UUID, UUID, BIGINT, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[])
        FROM PUBLIC;
    GRANT EXECUTE ON FUNCTION public.restore_missing_manifest_part(
        UUID, UUID, UUID, BIGINT, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[])
        TO audit_operator;
    GRANT EXECUTE ON FUNCTION public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]) TO audit_writer;
    REVOKE CREATE ON SCHEMA public FROM audit_writer;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
    EXECUTE 'REVOKE audit_writer FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT ops_outbox_owner TO ' || quote_ident(current_user);
    DROP FUNCTION IF EXISTS public.restore_missing_manifest_part(
        UUID, UUID, UUID, BIGINT, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[]);
    REVOKE EXECUTE ON FUNCTION public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]) FROM audit_writer;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd
