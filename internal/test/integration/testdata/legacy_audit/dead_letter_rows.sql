SELECT count(*)
  FROM audit.events_dlq
 WHERE position(convert_to($1::uuid::text, 'UTF8') IN payload) > 0
