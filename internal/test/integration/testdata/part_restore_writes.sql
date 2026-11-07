SELECT (SELECT count(*) FROM audit.event_parts WHERE org_id = $1 AND event_id = $2),
       (SELECT count(*) FROM public.ops_outbox WHERE event_id = $3)
