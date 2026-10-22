SELECT count(*)
  FROM audit.event_assemblies
 WHERE event_id = $1
   AND invalid_reason IS NOT NULL
