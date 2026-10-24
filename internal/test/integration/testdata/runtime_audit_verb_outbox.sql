SELECT event
  FROM public.ops_outbox
 WHERE event->>'verb' = $1
   AND event->'actor'->>'id' = $2
 ORDER BY created_at ASC, event_id ASC
