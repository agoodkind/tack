SELECT event
  FROM public.ops_outbox
 WHERE event->'entity'->>'id' = $1
    OR event->'context'->>'operation_id' = $1
 ORDER BY created_at, event_id
