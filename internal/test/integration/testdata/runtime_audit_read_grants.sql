SELECT
    EXISTS (
        SELECT 1
          FROM pg_auth_members m
          JOIN pg_roles owner_role ON owner_role.oid = m.roleid
          JOIN pg_roles member_role ON member_role.oid = m.member
         WHERE owner_role.rolname = 'ops_outbox_owner'
    ) AS owner_has_members,
    has_schema_privilege('ops_outbox_owner', 'public', 'CREATE') AS owner_creates_in_public,
    (SELECT count(*)
       FROM pg_proc p
       JOIN pg_namespace n ON n.oid = p.pronamespace
      WHERE n.nspname = 'public' AND p.proname LIKE 'ops\_read\_%'
        AND pg_get_userbyid(p.proowner) = 'ops_outbox_owner'
        AND p.prosecdef
        AND has_function_privilege('app_auth', p.oid, 'EXECUTE')
        AND has_function_privilege('tack_migrator', p.oid, 'EXECUTE')) AS executable_functions,
    (SELECT count(*)
       FROM pg_proc p
       JOIN pg_namespace n ON n.oid = p.pronamespace
      WHERE n.nspname = 'public' AND p.proname LIKE 'ops\_read\_%'
        AND has_function_privilege('public', p.oid, 'EXECUTE')) AS public_functions,
    pg_get_userbyid((SELECT relowner FROM pg_class WHERE oid = 'public.ops_read_state'::regclass))
        AS state_owner,
    has_table_privilege('app_auth', 'public.ops_read_state', 'SELECT,INSERT,UPDATE,DELETE')
        OR has_table_privilege('tack_migrator', 'public.ops_read_state', 'SELECT,INSERT,UPDATE,DELETE')
        OR has_table_privilege('app_auth', 'public.ops_outbox', 'INSERT,UPDATE,DELETE')
        OR has_table_privilege('tack_migrator', 'public.ops_outbox', 'INSERT,UPDATE,DELETE')
        AS table_rights
