SELECT substring(convert_to((context->'read')::text, 'UTF8') FROM 1 FOR $3) FROM audit.events
 WHERE org_id = $1 AND event_id = $2
 LIMIT 2
