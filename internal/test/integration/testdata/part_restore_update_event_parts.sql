UPDATE audit.events SET context = jsonb_set(context, '{read,manifest_parts}', '2'::jsonb) WHERE org_id = $1 AND event_id = $2
