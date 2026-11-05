SELECT substring(convert_to((context->'read')::text, 'UTF8') FROM 1 FOR $3),
       COALESCE(substring(context->>'audit_format_version' FROM 1 FOR 7), '0'),
       COALESCE(substring(context->>'operation_class' FROM 1 FOR 65), ''),
       COALESCE(substring(context->>'phase' FROM 1 FOR 65), ''),
       COALESCE(substring(outcome FROM 1 FOR 65), '')
  FROM audit.events
 WHERE org_id = $1 AND event_id = $2
 LIMIT 2
