SELECT error
  FROM audit.events_dlq
 WHERE topic = $1
