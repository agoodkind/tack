-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audit_part_restore_check_transport(
    p_org UUID, p_change_event_id UUID, p_change_header JSONB, p_change_parts BYTEA[])
RETURNS JSONB
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    change_event JSONB;
    envelope BYTEA := ''::BYTEA;
    envelope_text TEXT;
    envelope_json JSONB;
    part_message JSONB;
    part_payload BYTEA;
    part_text TEXT;
    part_total INTEGER;
    part_position INTEGER;
    integer_key TEXT;
    version_text TEXT;
    raw_texts TEXT[] := ARRAY[]::TEXT[];
    shard_input BYTEA;
    shard_crc BIGINT;
    expected_shard INTEGER;
    crc_bit INTEGER;
    max_part_bytes CONSTANT INTEGER := 65536;
    uuid_pattern CONSTANT TEXT := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
    transport_keys CONSTANT TEXT[] := ARRAY['audit_message', 'format_version', 'event_id', 'org_id', 'shard',
        'part_count', 'transport_digest', 'part_index', 'part_digest', 'payload', 'commit_version'];
    integer_keys CONSTANT TEXT[] := ARRAY['format_version', 'shard', 'part_count', 'part_index'];
BEGIN
    part_total := coalesce(array_length(p_change_parts, 1), 0);
    BEGIN
        IF p_change_header IS NULL OR jsonb_typeof(p_change_header) <> 'object' THEN
            RAISE EXCEPTION 'restore refused: the change header is not a JSON object' USING ERRCODE = 'TK620';
        END IF;
        IF coalesce(p_change_header ->> 'audit_message', '') = '' THEN
            IF part_total <> 0 THEN
                RAISE EXCEPTION 'restore refused: a single-record change event includes part messages' USING ERRCODE = 'TK620';
            END IF;
            change_event := p_change_header;
            version_text := p_change_header::TEXT;
        ELSE
            IF EXISTS (SELECT 1 FROM jsonb_object_keys(p_change_header) AS k WHERE k <> ALL (transport_keys)) THEN
                RAISE EXCEPTION 'restore refused: the change manifest has a field that the consumer does not decode' USING ERRCODE = 'TK620';
            END IF;
            FOREACH integer_key IN ARRAY integer_keys LOOP
                IF jsonb_typeof(p_change_header -> integer_key) = 'number' AND (p_change_header -> integer_key)::TEXT !~ '^-?[0-9]+$' THEN
                    RAISE EXCEPTION 'restore refused: the change message field % is not written as an integer', integer_key USING ERRCODE = 'TK620';
                END IF;
            END LOOP;
            IF jsonb_typeof(p_change_header -> 'commit_version') NOT IN ('string', 'null') THEN
                RAISE EXCEPTION 'restore refused: a change message has a commit_version that is not a string' USING ERRCODE = 'TK620';
            END IF;
            IF p_change_header ->> 'audit_message' <> 'event_manifest' OR p_change_header -> 'format_version' IS DISTINCT FROM '1'::JSONB THEN
                RAISE EXCEPTION 'restore refused: the change header is not a version 1 event manifest' USING ERRCODE = 'TK620';
            END IF;
            IF p_change_header ->> 'event_id' IS DISTINCT FROM p_change_event_id::TEXT THEN
                RAISE EXCEPTION 'restore refused: the change manifest names another event ID' USING ERRCODE = 'TK620';
            END IF;
            IF p_change_header ->> 'org_id' IS DISTINCT FROM p_org::TEXT THEN
                RAISE EXCEPTION 'restore refused: the change manifest names another organization' USING ERRCODE = 'TK620';
            END IF;
            IF part_total = 0 OR p_change_header -> 'part_count' IS DISTINCT FROM to_jsonb(part_total) THEN
                RAISE EXCEPTION 'restore refused: the change manifest part count differs from the supplied parts' USING ERRCODE = 'TK620';
            END IF;
            IF coalesce(p_change_header ->> 'transport_digest', '') !~ '^[0-9a-f]{64}$' THEN
                RAISE EXCEPTION 'restore refused: the change manifest transport digest is not a SHA-256 hex digest' USING ERRCODE = 'TK620';
            END IF;
            FOR part_position IN 1 .. part_total LOOP
                part_text := convert_from(p_change_parts[part_position], 'UTF8');
                part_message := part_text::JSONB;
                raw_texts := raw_texts || part_text;
                IF EXISTS (SELECT 1 FROM jsonb_object_keys(part_message) AS k WHERE k <> ALL (transport_keys)) THEN
                    RAISE EXCEPTION 'restore refused: change part % has a field that the consumer does not decode', part_position - 1
                        USING ERRCODE = 'TK620';
                END IF;
                FOREACH integer_key IN ARRAY integer_keys LOOP
                    IF part_message ? integer_key AND part_text !~ ('"' || integer_key || '"\s*:\s*-?[0-9]+\s*[,}]') THEN
                        RAISE EXCEPTION 'restore refused: the change message field % is not written as an integer', integer_key USING ERRCODE = 'TK620';
                    END IF;
                END LOOP;
                IF jsonb_typeof(part_message -> 'commit_version') NOT IN ('string', 'null') THEN
                    RAISE EXCEPTION 'restore refused: a change message has a commit_version that is not a string' USING ERRCODE = 'TK620';
                END IF;
                IF part_message ->> 'audit_message' IS DISTINCT FROM 'event_part' OR part_message -> 'format_version' IS DISTINCT FROM '1'::JSONB THEN
                    RAISE EXCEPTION 'restore refused: change part % is not a version 1 event part', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                IF part_message ->> 'event_id' IS DISTINCT FROM p_change_event_id::TEXT
                   OR part_message ->> 'org_id' IS DISTINCT FROM p_org::TEXT
                   OR part_message -> 'shard' IS DISTINCT FROM p_change_header -> 'shard'
                   OR part_message -> 'part_count' IS DISTINCT FROM p_change_header -> 'part_count'
                   OR part_message -> 'transport_digest' IS DISTINCT FROM p_change_header -> 'transport_digest' THEN
                    RAISE EXCEPTION 'restore refused: change part % does not match the manifest event, organization, shard, part count, or transport digest', part_position - 1
                        USING ERRCODE = 'TK620';
                END IF;
                IF coalesce(part_message -> 'part_index', '0'::JSONB) IS DISTINCT FROM to_jsonb(part_position - 1) THEN
                    RAISE EXCEPTION 'restore refused: change part % is not the part at that index', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                IF jsonb_typeof(part_message -> 'payload') IS DISTINCT FROM 'string' THEN
                    RAISE EXCEPTION 'restore refused: change part % does not have a payload', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                part_payload := decode(part_message ->> 'payload', 'base64');
                IF octet_length(part_payload) = 0 OR replace(encode(part_payload, 'base64'), E'\n', '') <> part_message ->> 'payload' THEN
                    RAISE EXCEPTION 'restore refused: change part % payload is not canonical base64 text', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                IF octet_length(part_payload) > max_part_bytes THEN
                    RAISE EXCEPTION 'restore refused: change part % is larger than the part size limit', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                IF part_message ->> 'part_digest' IS DISTINCT FROM encode(sha256(part_payload), 'hex') THEN
                    RAISE EXCEPTION 'restore refused: change part % does not match its digest', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                envelope := envelope || part_payload;
            END LOOP;
            IF p_change_header ->> 'transport_digest' <> encode(sha256(envelope), 'hex') THEN
                RAISE EXCEPTION 'restore refused: the change parts do not match the manifest transport digest' USING ERRCODE = 'TK620';
            END IF;
            envelope_text := convert_from(envelope, 'UTF8');
            IF envelope_text !~ '^\{"event":' OR envelope_text !~ ',"read_targets":\[\]\}\s*$' THEN
                RAISE EXCEPTION 'restore refused: the change envelope is not an event followed by an empty read target list' USING ERRCODE = 'TK620';
            END IF;
            envelope_json := envelope_text::JSONB;
            IF EXISTS (SELECT 1 FROM jsonb_object_keys(envelope_json) AS k WHERE k NOT IN ('event', 'read_targets'))
               OR envelope_json -> 'read_targets' IS DISTINCT FROM '[]'::JSONB THEN
                RAISE EXCEPTION 'restore refused: the change envelope is not an event followed by an empty read target list' USING ERRCODE = 'TK620';
            END IF;
            change_event := envelope_json -> 'event';
            version_text := envelope_text;
            raw_texts := raw_texts || envelope_text;
            PERFORM public.audit_part_restore_check_keys(raw_texts);
        END IF;

        IF change_event IS NULL OR jsonb_typeof(change_event) <> 'object' THEN
            RAISE EXCEPTION 'restore refused: the change event is not a JSON object' USING ERRCODE = 'TK620';
        END IF;
        IF jsonb_typeof(change_event #> '{context,audit_format_version}') = 'number'
           AND version_text !~ '"audit_format_version"\s*:\s*-?[0-9]+\s*[,}]' THEN
            RAISE EXCEPTION 'restore refused: the change message field audit_format_version is not written as an integer' USING ERRCODE = 'TK620';
        END IF;
        IF coalesce(p_change_header ->> 'audit_message', '') <> '' AND coalesce(change_event #>> '{actor,id}', '') ~ uuid_pattern THEN
            shard_input := decode(replace(change_event #>> '{actor,id}', '-', ''), 'hex')
                        || decode(replace(p_change_event_id::TEXT, '-', ''), 'hex');
            shard_crc := 4294967295;
            FOR part_position IN 0 .. octet_length(shard_input) - 1 LOOP
                shard_crc := shard_crc # get_byte(shard_input, part_position);
                FOR crc_bit IN 1 .. 8 LOOP
                    IF (shard_crc & 1) = 1 THEN
                        shard_crc := (shard_crc >> 1) # 3988292384;
                    ELSE
                        shard_crc := shard_crc >> 1;
                    END IF;
                END LOOP;
            END LOOP;
            expected_shard := ((shard_crc # 4294967295) & 255)::INTEGER;
            IF p_change_header -> 'shard' IS DISTINCT FROM to_jsonb(expected_shard) THEN
                RAISE EXCEPTION 'restore refused: the change manifest shard is not the shard of the actor and event' USING ERRCODE = 'TK620';
            END IF;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        IF SQLSTATE = 'TK620' THEN
            RAISE;
        END IF;
        RAISE EXCEPTION 'restore refused: the change event does not decode: %', SQLERRM USING ERRCODE = 'TK620';
    END;
    RETURN change_event;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT audit_writer TO ' || quote_ident(current_user);
    GRANT CREATE ON SCHEMA public TO audit_writer;
    ALTER FUNCTION public.audit_part_restore_check_transport(UUID, UUID, JSONB, BYTEA[]) OWNER TO audit_writer;
    REVOKE ALL ON FUNCTION public.audit_part_restore_check_transport(UUID, UUID, JSONB, BYTEA[]) FROM PUBLIC;
    REVOKE CREATE ON SCHEMA public FROM audit_writer;
    EXECUTE 'REVOKE audit_writer FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS public.audit_part_restore_check_transport(UUID, UUID, JSONB, BYTEA[]);
-- +goose StatementEnd
