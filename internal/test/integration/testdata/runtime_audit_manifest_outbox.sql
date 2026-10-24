SELECT event_id
  FROM public.ops_outbox
 WHERE event->>'audit_message' = 'event_manifest'
 ORDER BY created_at ASC, event_id ASC
