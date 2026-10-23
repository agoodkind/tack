UPDATE public.ops_read_state
   SET lease_deadline = now() - interval '1 second',
       admission_deadline = now() - interval '1 second'
 WHERE template -> 'context' ->> 'request_id' = $1 AND lease_deadline IS NOT NULL
RETURNING event_id
