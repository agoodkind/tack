UPDATE audit.events SET context = jsonb_set(context, '{read,payload_sha256}', '"invalid"'::jsonb)
 WHERE org_id = $1 AND event_id = $2
