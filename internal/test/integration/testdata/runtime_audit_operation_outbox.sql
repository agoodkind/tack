SELECT event
  FROM public.ops_outbox
 WHERE event->'context'->>'operation_id' = $1
    OR event->'context'->>'parent_operation_id' = $1
 ORDER BY created_at ASC, event_id ASC
