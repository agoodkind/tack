SELECT digest, payload FROM audit.event_parts WHERE org_id = $1 AND event_id = $2 AND part_index = 0
