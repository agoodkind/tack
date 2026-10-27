SELECT event
  FROM public.ops_outbox
 WHERE event->'actor'->>'id' = $1
 ORDER BY created_at ASC, event_id ASC
