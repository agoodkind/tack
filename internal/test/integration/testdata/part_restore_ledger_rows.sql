SELECT event_id, seq, prev_hash, row_hash, hash_version FROM audit.events WHERE org_id = $1 ORDER BY event_id
