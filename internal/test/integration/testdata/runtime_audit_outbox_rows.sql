SELECT count(*)
  FROM public.ops_outbox
 WHERE event_id = $1
