SELECT action, COALESCE(outcome, ''), COALESCE(error->>'code', '')
  FROM audit.events
 WHERE actor_id = $1
 ORDER BY event_time ASC, event_id ASC
