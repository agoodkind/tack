SELECT count(*)
  FROM public.ops_outbox
 WHERE event->>'audit_message' = 'event_manifest'
