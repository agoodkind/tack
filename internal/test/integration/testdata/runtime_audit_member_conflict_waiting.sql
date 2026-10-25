SELECT count(*)
FROM pg_stat_activity
WHERE state = 'active'
  AND pid <> pg_backend_pid()
  AND query LIKE '%INSERT INTO org_members (id, org_id, user_id, role)%'
