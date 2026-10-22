SELECT event_id, event
  FROM public.ops_outbox
 WHERE event->>'verb' = $1
   AND event->'entity'->>'id' = $2
