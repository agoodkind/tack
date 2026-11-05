UPDATE audit.events SET context = jsonb_set(context, '{read,unexpected}', 'true'::jsonb)
 WHERE org_id = $1 AND event_id = $2
