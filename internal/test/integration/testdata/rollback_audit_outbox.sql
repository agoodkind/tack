SELECT event->>'outcome' AS outcome
  FROM public.ops_outbox
 WHERE event->>'verb' = $1
   AND event->'actor'->>'id' = $2;
