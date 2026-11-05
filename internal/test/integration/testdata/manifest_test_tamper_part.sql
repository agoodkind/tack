UPDATE audit.event_parts SET payload = payload || decode('20', 'hex')
 WHERE org_id = $1 AND event_id = $2 AND part_index = 0
