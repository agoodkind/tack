UPDATE audit.event_parts SET payload = $3, digest = $4
 WHERE org_id = $1 AND event_id = $2 AND part_index = 0
