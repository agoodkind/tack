SELECT record
  FROM (
        SELECT 0 AS record_kind, part_index, message AS record
          FROM public.ops_outbox_parts
         WHERE event_id = $1
        UNION ALL
        SELECT 1 AS record_kind, 0 AS part_index, convert_to(event::text, 'UTF8') AS record
          FROM public.ops_outbox
         WHERE event_id = $1
       ) AS records
 ORDER BY record_kind, part_index
