SELECT count(*) FILTER (WHERE outcome = 'incomplete'), count(*)
  FROM public.ops_read_state
 WHERE event_id = ANY($1)
