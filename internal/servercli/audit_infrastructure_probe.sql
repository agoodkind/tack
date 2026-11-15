SELECT
    to_regclass('public.ops_outbox') IS NOT NULL,
    EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_audit_operator'),
    CASE
        WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'audit_operator') THEN false
        ELSE COALESCE(has_function_privilege(
            'audit_operator',
            to_regprocedure('public.ops_read_begin(uuid, uuid, smallint, jsonb)')::oid,
            'EXECUTE'), false)
    END
