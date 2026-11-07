-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.restore_missing_manifest_part(
    p_org UUID, p_event UUID, p_source UUID, p_root TEXT,
    p_row_hash BYTEA, p_seq BIGINT, p_shard SMALLINT,
    p_source_row_hash BYTEA, p_source_seq BIGINT, p_source_shard SMALLINT,
    p_change_event_id UUID, p_change_header JSONB, p_change_parts BYTEA[])
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, audit, public
AS $$
DECLARE
    target_matches BIGINT;
    source_matches BIGINT;
    source_digest TEXT;
    source_payload BYTEA;
    change_event JSONB;
    after_record JSONB;
    envelope BYTEA := ''::BYTEA;
    envelope_text TEXT;
    envelope_json JSONB;
    part_message JSONB;
    part_payload BYTEA;
    part_text TEXT;
    part_total INTEGER;
    part_position INTEGER;
    part_uri TEXT := 'tack:audit-event/' || p_event::TEXT || '/part/0';
    time_text TEXT;
    event_started TIMESTAMPTZ;
    event_completed TIMESTAMPTZ;
    unknown_section TEXT;
    reference_count INTEGER;
    shard_input BYTEA;
    shard_crc BIGINT;
    expected_shard INTEGER;
    crc_bit INTEGER;
    max_part_bytes CONSTANT INTEGER := 65536;
    nil_uuid CONSTANT TEXT := '00000000-0000-0000-0000-000000000000';
    uuid_pattern CONSTANT TEXT := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
    time_pattern CONSTANT TEXT := '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$';
    transport_keys CONSTANT TEXT[] := ARRAY['audit_message', 'format_version', 'event_id', 'org_id', 'shard',
        'part_count', 'transport_digest', 'part_index', 'part_digest', 'payload', 'commit_version'];
BEGIN
    IF p_source = p_event THEN
        RAISE EXCEPTION 'restore refused: the source event is the target event' USING ERRCODE = 'TK620';
    END IF;

    SELECT count(*) FILTER (WHERE e.event_id = p_event),
           count(*) FILTER (WHERE e.event_id = p_source)
      INTO target_matches, source_matches
      FROM audit.events e
     WHERE e.org_id = p_org
       AND e.context #>> '{read,manifest_sha256}' = p_root
       AND e.context #>> '{read,manifest_parts}' = '1'
       AND ((e.event_id = p_event AND e.shard = p_shard AND e.seq = p_seq AND e.row_hash = p_row_hash)
         OR (e.event_id = p_source AND e.shard = p_source_shard AND e.seq = p_source_seq AND e.row_hash = p_source_row_hash));
    IF target_matches <> 1 THEN
        RAISE EXCEPTION 'restore refused: the target event does not match the pinned org, row hash, seq, shard, and one-part root'
            USING ERRCODE = 'TK620';
    END IF;
    IF source_matches <> 1 THEN
        RAISE EXCEPTION 'restore refused: the source event does not match the pinned org, row hash, seq, shard, and one-part root'
            USING ERRCODE = 'TK620';
    END IF;

    IF EXISTS (SELECT 1 FROM audit.event_parts p WHERE p.org_id = p_org AND p.event_id = p_event) THEN
        RAISE EXCEPTION 'restore refused: the target event already has a part row' USING ERRCODE = 'TK620';
    END IF;

    SELECT p.digest, p.payload INTO source_digest, source_payload FROM audit.event_parts p
     WHERE p.org_id = p_org AND p.event_id = p_source AND p.part_index = 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'restore refused: the source event does not store part 0' USING ERRCODE = 'TK620';
    END IF;
    IF source_digest <> p_root OR encode(sha256(source_payload), 'hex') <> p_root THEN
        RAISE EXCEPTION 'restore refused: the source part does not hash to the declared root' USING ERRCODE = 'TK620';
    END IF;

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
        ELSE
            IF EXISTS (SELECT 1 FROM jsonb_object_keys(p_change_header) AS k WHERE k <> ALL (transport_keys)) THEN
                RAISE EXCEPTION 'restore refused: the change manifest has a field that the consumer does not decode' USING ERRCODE = 'TK620';
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
                part_message := convert_from(p_change_parts[part_position], 'UTF8')::JSONB;
                IF EXISTS (SELECT 1 FROM jsonb_object_keys(part_message) AS k WHERE k <> ALL (transport_keys)) THEN
                    RAISE EXCEPTION 'restore refused: change part % has a field that the consumer does not decode', part_position - 1
                        USING ERRCODE = 'TK620';
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
                part_text := part_message ->> 'payload';
                IF jsonb_typeof(part_message -> 'payload') IS DISTINCT FROM 'string' THEN
                    RAISE EXCEPTION 'restore refused: change part % does not have a payload', part_position - 1 USING ERRCODE = 'TK620';
                END IF;
                part_payload := decode(part_text, 'base64');
                IF octet_length(part_payload) = 0 OR replace(encode(part_payload, 'base64'), E'\n', '') <> part_text THEN
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
        END IF;

        IF change_event IS NULL OR jsonb_typeof(change_event) <> 'object' THEN
            RAISE EXCEPTION 'restore refused: the change event is not a JSON object' USING ERRCODE = 'TK620';
        END IF;
        SELECT v.section INTO unknown_section FROM (VALUES
            ('event', change_event, ARRAY['verb', 'event_id', 'actor', 'entity', 'context', 'delta', 'outcome', 'error',
                'idempotency_key', 'occurred_at', 'extra', 'protected']),
            ('actor', change_event -> 'actor', ARRAY['type', 'id', 'email', 'name', 'session_id', 'ip', 'ua', 'request_id',
                'api_token_label']),
            ('entity', change_event -> 'entity', ARRAY['type', 'node_type', 'id', 'identifier', 'name']),
            ('context', change_event -> 'context', ARRAY['org_id', 'workspace_id', 'scope_id', 'parent_id', 'request_id',
                'trace_id', 'source', 'tool', 'rpc', 'reason', 'api_token_id', 'commit_version', 'audit_format_version',
                'operation_class', 'phase', 'operation_id', 'parent_operation_id', 'started_at', 'completed_at', 'target',
                'read', 'committed_steps', 'selection', 'http_method', 'request_path']),
            ('target', change_event #> '{context,target}', ARRAY['kind', 'node_type', 'reference', 'name', 'uri',
                'relation_type', 'id', 'source_id', 'target_id', 'owner_id']),
            ('delta', change_event -> 'delta', ARRAY['before', 'after', 'changed']),
            ('protected', change_event #> '{protected,0}', ARRAY['ref', 'subject_id', 'value'])
        ) AS v(section, body, allowed)
         WHERE jsonb_typeof(v.body) = 'object'
           AND EXISTS (SELECT 1 FROM jsonb_object_keys(v.body) AS k WHERE k <> ALL (v.allowed))
         LIMIT 1;
        IF unknown_section IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event has a field in % that the consumer does not decode', unknown_section
                USING ERRCODE = 'TK620';
        END IF;

        IF change_event ->> 'event_id' IS DISTINCT FROM p_change_event_id::TEXT THEN
            RAISE EXCEPTION 'restore refused: the change event ID is not the derived change ID' USING ERRCODE = 'TK620';
        END IF;
        IF change_event ->> 'verb' IS DISTINCT FROM 'audit.event_part_create' THEN
            RAISE EXCEPTION 'restore refused: the change event verb is not audit.event_part_create' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{context,org_id}' IS DISTINCT FROM p_org::TEXT THEN
            RAISE EXCEPTION 'restore refused: the change event organization is not the restored organization' USING ERRCODE = 'TK620';
        END IF;
        FOREACH time_text IN ARRAY ARRAY[change_event ->> 'occurred_at', change_event #>> '{context,started_at}',
                                         change_event #>> '{context,completed_at}'] LOOP
            IF time_text IS NULL OR time_text !~ time_pattern OR time_text::TIMESTAMPTZ <= '0001-01-01T00:00:00Z'::TIMESTAMPTZ THEN
                RAISE EXCEPTION 'restore refused: the change event lacks a valid occurrence, start, or completion time' USING ERRCODE = 'TK620';
            END IF;
        END LOOP;
        event_started := (change_event #>> '{context,started_at}')::TIMESTAMPTZ;
        event_completed := (change_event #>> '{context,completed_at}')::TIMESTAMPTZ;
        IF event_completed < event_started THEN
            RAISE EXCEPTION 'restore refused: the change event completion time precedes its start time' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #> '{context,audit_format_version}' IS DISTINCT FROM '2'::JSONB THEN
            RAISE EXCEPTION 'restore refused: the change event is not a format 2 event' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{context,operation_class}' IS DISTINCT FROM 'database_change'
           OR change_event #>> '{context,phase}' IS DISTINCT FROM 'change' THEN
            RAISE EXCEPTION 'restore refused: the change event is not a database change in the change phase' USING ERRCODE = 'TK620';
        END IF;
        IF change_event ->> 'outcome' IS DISTINCT FROM 'completed'
           OR coalesce(jsonb_typeof(change_event -> 'error'), 'null') <> 'null'
           OR coalesce(change_event #> '{context,committed_steps}', '[]'::JSONB) IS DISTINCT FROM '[]'::JSONB THEN
            RAISE EXCEPTION 'restore refused: the change event does not have the completed outcome without an error or committed steps'
                USING ERRCODE = 'TK620';
        END IF;
        IF coalesce(jsonb_typeof(change_event #> '{context,read}'), 'null') <> 'null' THEN
            RAISE EXCEPTION 'restore refused: the change event includes read evidence' USING ERRCODE = 'TK620';
        END IF;
        IF coalesce(change_event #>> '{context,operation_id}', nil_uuid) !~ uuid_pattern
           OR coalesce(change_event #>> '{context,operation_id}', nil_uuid) = nil_uuid THEN
            RAISE EXCEPTION 'restore refused: the change event lacks an operation ID' USING ERRCODE = 'TK620';
        END IF;
        IF coalesce(nullif(change_event #>> '{context,request_id}', ''), nullif(change_event #>> '{actor,request_id}', '')) IS NULL
           OR nullif(change_event #>> '{context,trace_id}', '') IS NULL THEN
            RAISE EXCEPTION 'restore refused: the change event lacks a request ID or a trace ID' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{actor,type}' IS NULL
           OR change_event #>> '{actor,type}' NOT IN ('user', 'service', 'system', 'api_token', 'operator')
           OR coalesce(change_event #>> '{actor,id}', nil_uuid) !~ uuid_pattern
           OR coalesce(change_event #>> '{actor,id}', nil_uuid) = nil_uuid THEN
            RAISE EXCEPTION 'restore refused: the change event lacks a known actor kind and an actor ID' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{context,target,kind}' IS DISTINCT FROM 'resource'
           OR change_event #>> '{context,target,node_type}' IS DISTINCT FROM 'audit_event_part'
           OR change_event #>> '{context,target,id}' IS DISTINCT FROM p_event::TEXT
           OR change_event #>> '{context,target,uri}' IS DISTINCT FROM part_uri
           OR change_event #> '{context,target,owner_id}' IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event target is not part 0 of the restored event' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{entity,type}' IS DISTINCT FROM change_event #>> '{context,target,kind}'
           OR change_event #>> '{entity,id}' IS DISTINCT FROM change_event #>> '{context,target,id}' THEN
            RAISE EXCEPTION 'restore refused: the change event entity differs from its target' USING ERRCODE = 'TK620';
        END IF;
        IF change_event #> '{delta,before}' IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event includes a before value for a created part' USING ERRCODE = 'TK620';
        END IF;
        after_record := change_event #> '{delta,after}';
        IF jsonb_typeof(after_record) IS DISTINCT FROM 'object'
           OR after_record ->> 'org_id' IS DISTINCT FROM p_org::TEXT
           OR after_record ->> 'event_id' IS DISTINCT FROM p_event::TEXT
           OR after_record -> 'part_index' IS DISTINCT FROM '0'::JSONB
           OR after_record ->> 'digest' IS DISTINCT FROM p_root THEN
            RAISE EXCEPTION 'restore refused: the change event after record does not name the restored part and manifest digest'
                USING ERRCODE = 'TK620';
        END IF;
        SELECT count(*) INTO reference_count FROM jsonb_each(after_record) AS member
         WHERE jsonb_typeof(member.value) = 'object' AND member.value ? 'pii_ref'
           AND (SELECT count(*) FROM jsonb_object_keys(member.value)) = 1;
        IF reference_count <> 1
           OR jsonb_typeof(after_record -> 'payload') IS DISTINCT FROM 'object'
           OR coalesce(after_record #>> '{payload,pii_ref}', nil_uuid) !~ uuid_pattern
           OR coalesce(after_record #>> '{payload,pii_ref}', nil_uuid) = nil_uuid
           OR (CASE WHEN jsonb_typeof(change_event -> 'protected') = 'array' THEN jsonb_array_length(change_event -> 'protected') ELSE -1 END) <> 1
           OR change_event #>> '{protected,0,ref}' IS DISTINCT FROM after_record #>> '{payload,pii_ref}'
           OR change_event #>> '{protected,0,subject_id}' IS DISTINCT FROM p_event::TEXT
           OR jsonb_typeof(change_event #> '{protected,0,value}') IS DISTINCT FROM 'string' THEN
            RAISE EXCEPTION 'restore refused: the change event does not include one protected payload reference for the restored event'
                USING ERRCODE = 'TK620';
        END IF;
        IF decode(change_event #>> '{protected,0,value}', 'base64') IS DISTINCT FROM source_payload THEN
            RAISE EXCEPTION 'restore refused: the protected payload of the change event differs from the restored part payload'
                USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{extra,source_event_id}' IS DISTINCT FROM p_source::TEXT THEN
            RAISE EXCEPTION 'restore refused: the change event does not name the source event' USING ERRCODE = 'TK620';
        END IF;

        IF coalesce(p_change_header ->> 'audit_message', '') <> '' THEN
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

    INSERT INTO audit.event_parts (org_id, event_id, part_index, digest, payload)
    VALUES (p_org, p_event, 0, source_digest, source_payload);

    PERFORM public.ops_outbox_write_encoded(p_change_event_id, p_change_header, p_change_parts);
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
        UUID, UUID, UUID, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[])
        OWNER TO audit_writer;
    REVOKE ALL ON FUNCTION public.restore_missing_manifest_part(
        UUID, UUID, UUID, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[])
        FROM PUBLIC;
    GRANT EXECUTE ON FUNCTION public.restore_missing_manifest_part(
        UUID, UUID, UUID, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[])
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
        UUID, UUID, UUID, TEXT, BYTEA, BIGINT, SMALLINT, BYTEA, BIGINT, SMALLINT, UUID, JSONB, BYTEA[]);
    REVOKE EXECUTE ON FUNCTION public.ops_outbox_write_encoded(UUID, JSONB, BYTEA[]) FROM audit_writer;
    EXECUTE 'REVOKE ops_outbox_owner FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd
