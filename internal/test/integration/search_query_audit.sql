SELECT count(*), count(*) FILTER (WHERE outcome = $4)
FROM audit.events
WHERE org_id = $1 AND action = $2
  AND context->>'request_id' = $3
  AND context->>'tool' = 'tack_search'
