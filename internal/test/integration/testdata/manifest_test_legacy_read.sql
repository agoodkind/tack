UPDATE audit.events SET context = jsonb_set(context, '{audit_format_version}', '0'::jsonb)
 WHERE org_id = $1 AND event_id = $2
