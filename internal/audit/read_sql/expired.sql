SELECT out_event_id, out_generation, out_lease_deadline, out_template, out_observed_at
  FROM public.ops_read_expired($1, $2)
