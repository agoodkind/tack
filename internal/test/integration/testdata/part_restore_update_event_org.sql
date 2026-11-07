UPDATE audit.events SET org_id = $3 WHERE org_id = $1 AND event_id = $2
