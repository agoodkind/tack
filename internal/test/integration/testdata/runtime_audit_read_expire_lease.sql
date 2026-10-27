UPDATE public.ops_read_state
   SET lease_deadline = now() - interval '1 second',
       admission_deadline = now() - interval '1 second'
 WHERE event_id = ANY($1) AND lease_deadline IS NOT NULL
