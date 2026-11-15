SELECT role.rolcanlogin,
       COALESCE(array_agg(base.rolname::text ORDER BY base.rolname)
           FILTER (WHERE base.rolname::text = ANY($2::text[])), '{}'::text[])
  FROM pg_roles role
  LEFT JOIN pg_auth_members membership ON membership.member = role.oid
  LEFT JOIN pg_roles base ON base.oid = membership.roleid
 WHERE role.rolname = $1
 GROUP BY role.rolcanlogin
