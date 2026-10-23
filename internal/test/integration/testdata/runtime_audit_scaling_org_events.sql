SELECT count(*), coalesce(sum(pg_column_size(e.*)), 0)::bigint
  FROM audit.events e
 WHERE e.org_id = $1
