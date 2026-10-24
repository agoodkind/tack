SELECT out_generation, out_admission_deadline
  FROM public.ops_read_begin($1, $2, $3, $4)
