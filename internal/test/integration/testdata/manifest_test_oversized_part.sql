UPDATE audit.event_parts SET payload = convert_to(repeat('a', 65537), 'UTF8')
 WHERE org_id = $1 AND event_id = $2 AND part_index = 0
