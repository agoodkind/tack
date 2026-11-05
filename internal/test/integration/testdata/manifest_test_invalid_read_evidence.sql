UPDATE audit.events SET context = context #- '{read,payload_format}'
 WHERE org_id = $1 AND event_id = $2
