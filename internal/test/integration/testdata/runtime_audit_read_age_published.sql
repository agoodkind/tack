UPDATE public.ops_read_state
   SET published_at = published_at - interval '2 hours'
 WHERE published_at IS NOT NULL
