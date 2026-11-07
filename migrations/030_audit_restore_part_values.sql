-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audit_part_restore_check_values(
    p_org UUID, p_event UUID, p_source UUID, p_root TEXT, p_change_event_id UUID,
    p_change_event JSONB, p_source_payload BYTEA)
RETURNS VOID
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    change_event JSONB := p_change_event;
    after_record JSONB;
    time_text TEXT;
    event_started TIMESTAMPTZ;
    event_completed TIMESTAMPTZ;
    reference_count INTEGER;
    part_uri CONSTANT TEXT := 'tack:audit-event/' || p_event::TEXT || '/part/0';
    nil_uuid CONSTANT TEXT := '00000000-0000-0000-0000-000000000000';
    uuid_pattern CONSTANT TEXT := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
    time_pattern CONSTANT TEXT := '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$';
BEGIN
    BEGIN
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
        IF decode(change_event #>> '{protected,0,value}', 'base64') IS DISTINCT FROM p_source_payload THEN
            RAISE EXCEPTION 'restore refused: the protected payload of the change event differs from the restored part payload'
                USING ERRCODE = 'TK620';
        END IF;
        IF change_event #>> '{extra,source_event_id}' IS DISTINCT FROM p_source::TEXT THEN
            RAISE EXCEPTION 'restore refused: the change event does not name the source event' USING ERRCODE = 'TK620';
        END IF;
    EXCEPTION WHEN OTHERS THEN
        IF SQLSTATE = 'TK620' THEN
            RAISE;
        END IF;
        RAISE EXCEPTION 'restore refused: the change event does not decode: %', SQLERRM USING ERRCODE = 'TK620';
    END;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE 'GRANT audit_writer TO ' || quote_ident(current_user);
    GRANT CREATE ON SCHEMA public TO audit_writer;
    ALTER FUNCTION public.audit_part_restore_check_values(UUID, UUID, UUID, TEXT, UUID, JSONB, BYTEA) OWNER TO audit_writer;
    REVOKE ALL ON FUNCTION public.audit_part_restore_check_values(UUID, UUID, UUID, TEXT, UUID, JSONB, BYTEA) FROM PUBLIC;
    REVOKE CREATE ON SCHEMA public FROM audit_writer;
    EXECUTE 'REVOKE audit_writer FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS public.audit_part_restore_check_values(UUID, UUID, UUID, TEXT, UUID, JSONB, BYTEA);
-- +goose StatementEnd
