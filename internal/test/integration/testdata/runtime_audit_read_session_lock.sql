UPDATE public.ops_read_state
   SET owner_id = owner_id
 WHERE event_id = $1
