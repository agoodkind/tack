SELECT (SELECT count(*) FROM audit.events),
       (SELECT count(*) FROM audit.chain_heads),
       (SELECT count(*) FROM audit.projected_events),
       (SELECT count(*) FROM audit.pii),
       (SELECT count(*) FROM audit.event_parts),
       (SELECT count(*) FROM public.ops_outbox WHERE event_id = $1)
