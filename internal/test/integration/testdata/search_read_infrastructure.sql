SELECT to_regclass('public.ops_outbox') IS NOT NULL,
       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_audit_operator'),
       has_function_privilege('audit_operator', 'public.ops_read_begin(uuid, uuid, smallint, jsonb)', 'EXECUTE');
