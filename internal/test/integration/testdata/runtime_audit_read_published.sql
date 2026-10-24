SELECT count(*)
  FROM public.ops_read_state
 WHERE event_id = $1 AND published_at IS NOT NULL
