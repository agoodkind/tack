INSERT INTO public.ops_outbox (event_id, event) VALUES ($1, '{"synthetic":"duplicate"}'::jsonb)
