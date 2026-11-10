SELECT has_table_privilege('audit_writer', 'audit.events', 'SELECT'),
       has_function_privilege('audit_writer', 'public.ops_outbox_write_encoded(uuid, jsonb, bytea[])', 'EXECUTE')
