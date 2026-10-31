UPDATE public.ops_read_state
   SET lease_deadline = now() - interval '1 second',
       admission_deadline = now() - interval '1 second'
 WHERE template->'actor'->>'id' = $1
 RETURNING event_id;
