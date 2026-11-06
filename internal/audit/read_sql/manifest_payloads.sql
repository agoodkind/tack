SELECT part_index, substring(digest FROM 1 FOR 65), substring(payload FROM 1 FOR $3)
 FROM audit.event_parts
 WHERE org_id = $1 AND event_id = $2
 ORDER BY part_index ASC
