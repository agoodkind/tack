SELECT r.rolname::text,
       r.rolcanlogin,
       COALESCE(
           array_agg(b.rolname::text ORDER BY b.rolname) FILTER (WHERE b.rolname::text = ANY($2)),
           '{}'::text[]
       )
  FROM pg_roles r
  LEFT JOIN pg_auth_members m ON m.member = r.oid
  LEFT JOIN pg_roles b ON b.oid = m.roleid
 WHERE r.rolname::text = ANY($1)
 GROUP BY r.rolname, r.rolcanlogin
