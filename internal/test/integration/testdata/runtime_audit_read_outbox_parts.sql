SELECT message
  FROM public.ops_outbox_parts
 WHERE event_id = $1
 ORDER BY part_index ASC
