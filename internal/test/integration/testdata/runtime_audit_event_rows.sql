SELECT count(*)
  FROM audit.events
 WHERE event_id = $1
