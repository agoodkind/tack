SELECT count(*)
  FROM audit.events
 WHERE action = $1
   AND event_time >= $2
