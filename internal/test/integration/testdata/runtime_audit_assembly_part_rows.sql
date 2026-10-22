SELECT count(*)
  FROM audit.event_assembly_parts
 WHERE event_id = $1
