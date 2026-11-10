-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.audit_part_restore_check_shape(p_event JSONB)
RETURNS VOID
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    mismatch TEXT;
    uuid_pattern CONSTANT TEXT := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
BEGIN
    BEGIN
        IF p_event IS NULL OR jsonb_typeof(p_event) <> 'object' THEN
            RAISE EXCEPTION 'restore refused: the change event is not a JSON object' USING ERRCODE = 'TK620';
        END IF;
        SELECT v.section INTO mismatch FROM (VALUES
            ('event', p_event, ARRAY['verb', 'event_id', 'actor', 'entity', 'context', 'delta', 'outcome', 'error',
                'idempotency_key', 'occurred_at', 'extra', 'protected']),
            ('actor', p_event -> 'actor', ARRAY['type', 'id', 'email', 'name', 'session_id', 'ip', 'ua', 'request_id',
                'api_token_label']),
            ('entity', p_event -> 'entity', ARRAY['type', 'node_type', 'id', 'identifier', 'name']),
            ('context', p_event -> 'context', ARRAY['org_id', 'workspace_id', 'scope_id', 'parent_id', 'request_id',
                'trace_id', 'source', 'tool', 'rpc', 'reason', 'api_token_id', 'commit_version', 'audit_format_version',
                'operation_class', 'phase', 'operation_id', 'parent_operation_id', 'started_at', 'completed_at', 'target',
                'read', 'committed_steps', 'selection', 'http_method', 'request_path']),
            ('target', p_event #> '{context,target}', ARRAY['kind', 'node_type', 'reference', 'name', 'uri',
                'relation_type', 'id', 'source_id', 'target_id', 'owner_id']),
            ('delta', p_event -> 'delta', ARRAY['before', 'after', 'changed']),
            ('protected', p_event #> '{protected,0}', ARRAY['ref', 'subject_id', 'value'])
        ) AS v(section, body, allowed)
         WHERE jsonb_typeof(v.body) = 'object'
           AND EXISTS (SELECT 1 FROM jsonb_object_keys(v.body) AS k WHERE k <> ALL (v.allowed))
         LIMIT 1;
        IF mismatch IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event has a field in % that the consumer does not decode', mismatch
                USING ERRCODE = 'TK620';
        END IF;
        SELECT v.section || '.' || f.field INTO mismatch FROM (VALUES
            ('event', p_event, 'string', ARRAY['verb', 'event_id', 'outcome', 'idempotency_key', 'occurred_at']),
            ('event', p_event, 'object', ARRAY['actor', 'entity', 'context', 'delta', 'error']),
            ('event', p_event, 'array', ARRAY['protected']),
            ('actor', p_event -> 'actor', 'string', ARRAY['type', 'id', 'email', 'name', 'session_id', 'ip', 'ua',
                'request_id', 'api_token_label']),
            ('entity', p_event -> 'entity', 'string', ARRAY['type', 'node_type', 'id', 'identifier', 'name']),
            ('context', p_event -> 'context', 'string', ARRAY['org_id', 'workspace_id', 'scope_id', 'parent_id',
                'request_id', 'trace_id', 'source', 'tool', 'rpc', 'reason', 'api_token_id', 'commit_version',
                'operation_class', 'phase', 'operation_id', 'parent_operation_id', 'started_at', 'completed_at',
                'http_method', 'request_path']),
            ('context', p_event -> 'context', 'number', ARRAY['audit_format_version']),
            ('context', p_event -> 'context', 'object', ARRAY['target', 'read', 'selection']),
            ('context', p_event -> 'context', 'array', ARRAY['committed_steps']),
            ('target', p_event #> '{context,target}', 'string', ARRAY['kind', 'node_type', 'reference', 'name', 'uri',
                'relation_type', 'id', 'source_id', 'target_id', 'owner_id']),
            ('delta', p_event -> 'delta', 'array', ARRAY['changed']),
            ('protected', p_event #> '{protected,0}', 'string', ARRAY['ref', 'subject_id'])
        ) AS v(section, body, kind, fields)
        CROSS JOIN LATERAL unnest(v.fields) AS f(field)
         WHERE jsonb_typeof(v.body) = 'object'
           AND jsonb_typeof(v.body -> f.field) NOT IN ('null', v.kind)
         LIMIT 1;
        IF mismatch IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event field % has the wrong JSON type', mismatch USING ERRCODE = 'TK620';
        END IF;
        SELECT a.name INTO mismatch FROM (VALUES
            ('context.committed_steps', p_event #> '{context,committed_steps}'),
            ('delta.changed', p_event #> '{delta,changed}')
        ) AS a(name, items)
        CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(a.items) = 'array' THEN a.items ELSE '[]'::JSONB END) AS e(item)
         WHERE jsonb_typeof(e.item) NOT IN ('string', 'null')
         LIMIT 1;
        IF mismatch IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event list % has an element that is not a string', mismatch USING ERRCODE = 'TK620';
        END IF;
        SELECT v.section || '.' || f.field INTO mismatch FROM (VALUES
            ('actor', p_event -> 'actor', ARRAY['id']),
            ('entity', p_event -> 'entity', ARRAY['id']),
            ('context', p_event -> 'context', ARRAY['org_id', 'workspace_id', 'scope_id', 'parent_id', 'api_token_id',
                'operation_id', 'parent_operation_id']),
            ('target', p_event #> '{context,target}', ARRAY['id', 'source_id', 'target_id', 'owner_id']),
            ('protected', p_event #> '{protected,0}', ARRAY['ref', 'subject_id'])
        ) AS v(section, body, fields)
        CROSS JOIN LATERAL unnest(v.fields) AS f(field)
         WHERE jsonb_typeof(v.body -> f.field) = 'string' AND v.body ->> f.field !~ uuid_pattern
         LIMIT 1;
        IF mismatch IS NOT NULL THEN
            RAISE EXCEPTION 'restore refused: the change event field % is not a UUID', mismatch USING ERRCODE = 'TK620';
        END IF;
        IF coalesce(jsonb_typeof(p_event #> '{context,read}'), 'null') <> 'null'
           OR coalesce(jsonb_typeof(p_event #> '{context,selection}'), 'null') <> 'null' THEN
            RAISE EXCEPTION 'restore refused: the change event includes read evidence or a selection' USING ERRCODE = 'TK620';
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
    ALTER FUNCTION public.audit_part_restore_check_shape(JSONB) OWNER TO audit_writer;
    REVOKE ALL ON FUNCTION public.audit_part_restore_check_shape(JSONB) FROM PUBLIC;
    REVOKE CREATE ON SCHEMA public FROM audit_writer;
    EXECUTE 'REVOKE audit_writer FROM ' || quote_ident(current_user);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP FUNCTION IF EXISTS public.audit_part_restore_check_shape(JSONB);
-- +goose StatementEnd
