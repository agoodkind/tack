SELECT p.proname,
       pg_get_userbyid(p.proowner),
       coalesce((SELECT array_agg(CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END)
                   FROM aclexplode(p.proacl) a WHERE a.privilege_type = 'EXECUTE'), '{}'::text[]),
       has_function_privilege('audit_operator', p.oid, 'EXECUTE')
  FROM pg_proc p
  JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'public' AND p.proname LIKE 'audit\_part\_restore\_check\_%'
 ORDER BY p.proname
