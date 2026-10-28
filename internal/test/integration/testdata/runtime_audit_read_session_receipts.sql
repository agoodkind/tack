INSERT INTO public.ops_read_state
    (event_id, bucket, owner_id, generation, admission_deadline, digest, outcome, published_at)
SELECT receipt, $2::smallint, receipt, 1, now() - interval '2 hours', 'runtime-audit-session-role',
       'completed', now() - interval '2 hours'
  FROM unnest($1::uuid[]) AS receipt
