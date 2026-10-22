SELECT count(*)
  FROM audit.completed_transports
 WHERE event_id = $1
