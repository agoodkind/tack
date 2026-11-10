UPDATE audit.events SET context = jsonb_set(context, '{read,manifest_sha256}', to_jsonb($3::text)) WHERE org_id = $1 AND event_id = $2
