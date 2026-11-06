SELECT event_id FROM audit.events
 WHERE org_id = $1 AND event_time >= $2 AND event_time <= $3
   AND context->>'request_id' = $4 AND context->>'operation_class' = 'read'
 LIMIT 2
