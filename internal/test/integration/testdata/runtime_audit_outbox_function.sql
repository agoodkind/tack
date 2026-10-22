SELECT pg_has_role('tack_migrator', 'ops_outbox_owner', 'MEMBER'),
       pg_get_userbyid(proowner),
       has_function_privilege('ops_outbox_owner', oid, 'EXECUTE')
  FROM pg_proc
 WHERE proname = 'ops_outbox_write_encoded'
