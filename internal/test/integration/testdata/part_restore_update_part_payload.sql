UPDATE audit.event_parts SET payload = $3 WHERE org_id = $1 AND event_id = $2
