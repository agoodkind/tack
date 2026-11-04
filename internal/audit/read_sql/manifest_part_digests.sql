SELECT part_index, digest FROM audit.event_parts
 WHERE org_id = $1 AND event_id = $2
 ORDER BY part_index ASC
