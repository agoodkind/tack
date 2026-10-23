SELECT (SELECT count(*) FROM public.ops_outbox) + (SELECT count(*) FROM public.ops_outbox_parts)
