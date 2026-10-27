INSERT INTO public.ops_read_state (event_id, bucket, owner_id, generation, admission_deadline) VALUES ('019e6b4a-0000-7000-8000-0000000000a1', 0, '019e6b4a-0000-7000-8000-0000000000a2', 1, now());
UPDATE public.ops_read_state SET generation = 2;
DELETE FROM public.ops_read_state;
INSERT INTO public.ops_outbox (event_id, event) VALUES ('019e6b4a-0000-7000-8000-0000000000a3', '{}');
UPDATE public.ops_outbox SET event = '{}';
DELETE FROM public.ops_outbox;
