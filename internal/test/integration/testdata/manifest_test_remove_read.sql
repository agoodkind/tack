UPDATE audit.events SET context = context - 'read'
 WHERE org_id = $1 AND event_id = $2
