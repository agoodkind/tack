INSERT INTO audit.events (
    org_id, shard, event_time, event_id, seq, actor_id, actor_kind, action, entity_kind, entity_id,
    context, delta, prev_hash, row_hash, idempotency_key)
VALUES (
    $1, $2, now(), $3, $4, $5, 1, 'audit.read', 'resource', $5,
    jsonb_build_object('read', jsonb_build_object('manifest_sha256', $6::text, 'manifest_parts', 1, 'count', 25)),
    '{}'::jsonb, $7, $8, '')
