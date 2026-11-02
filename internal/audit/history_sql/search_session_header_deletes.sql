SELECT count(event_id)
  FROM audit.events
 WHERE org_id = $1
   AND entity_kind = $2
   AND entity_id = $3
   AND action = $4
   AND outcome = $5
   AND context->>'phase' = $6
   AND context->'target'->>'node_type' = $7
