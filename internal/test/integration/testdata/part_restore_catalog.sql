SELECT p.prosecdef,
       pg_get_userbyid(p.proowner),
       coalesce(p.proconfig, '{}'::text[]),
       coalesce((SELECT array_agg(CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END)
                   FROM aclexplode(p.proacl) a WHERE a.privilege_type = 'EXECUTE'), '{}'::text[]),
       p.proacl IS NOT NULL
  FROM pg_proc p
  JOIN pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = 'public' AND p.proname = 'restore_missing_manifest_part'
