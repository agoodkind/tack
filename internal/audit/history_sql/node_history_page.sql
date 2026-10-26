-- One page of the ledger rows of one node in one org, newest first.
--
-- $1 org_id, $2 node ID as uuid, $3 node ID as text, $4 oldest event_time
-- (inclusive), $5 $6 $7 the (event_time, seq, event_id) position that every
-- returned row precedes, $8 page size.
--
-- Each branch reads one index and stops at the page size. The first branch
-- reads the node's own rows through events_entity_time. The second branch
-- reads, through the same index, the legacy relationship rows that store the
-- node as the source in entity_id with entity_kind 'relationship'. Format 2
-- relationship rows store the zero UUID in entity_id, and the second branch
-- does not return them. The third and fourth branches read format 2
-- relationship rows through events_relationship_source_time and
-- events_relationship_target_time. The fourth branch skips a relationship
-- from the node to itself, which the third branch already returns.
(
    SELECT org_id, event_time, event_id, seq, shard,
           actor_id, actor_kind, action, outcome, entity_kind, entity_id,
           context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
           idempotency_key
      FROM audit.events
     WHERE org_id = $1 AND entity_kind = 'node' AND entity_id = $2
       AND event_time >= $4 AND event_time <= $5
       AND (event_time, seq, event_id) < ($5, $6, $7)
     ORDER BY event_time DESC, seq DESC, event_id DESC
     LIMIT $8
)
UNION ALL
(
    SELECT org_id, event_time, event_id, seq, shard,
           actor_id, actor_kind, action, outcome, entity_kind, entity_id,
           context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
           idempotency_key
      FROM audit.events
     WHERE org_id = $1 AND entity_kind = 'relationship' AND entity_id = $2
       AND event_time >= $4 AND event_time <= $5
       AND (event_time, seq, event_id) < ($5, $6, $7)
     ORDER BY event_time DESC, seq DESC, event_id DESC
     LIMIT $8
)
UNION ALL
(
    SELECT org_id, event_time, event_id, seq, shard,
           actor_id, actor_kind, action, outcome, entity_kind, entity_id,
           context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
           idempotency_key
      FROM audit.events
     WHERE org_id = $1 AND context->'target'->>'kind' = 'relationship'
       AND context->'target'->>'source_id' = $3
       AND event_time >= $4 AND event_time <= $5
       AND (event_time, seq, event_id) < ($5, $6, $7)
     ORDER BY event_time DESC, seq DESC, event_id DESC
     LIMIT $8
)
UNION ALL
(
    SELECT org_id, event_time, event_id, seq, shard,
           actor_id, actor_kind, action, outcome, entity_kind, entity_id,
           context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
           idempotency_key
      FROM audit.events
     WHERE org_id = $1 AND context->'target'->>'kind' = 'relationship'
       AND context->'target'->>'target_id' = $3
       AND context->'target'->>'source_id' <> $3
       AND event_time >= $4 AND event_time <= $5
       AND (event_time, seq, event_id) < ($5, $6, $7)
     ORDER BY event_time DESC, seq DESC, event_id DESC
     LIMIT $8
)
ORDER BY event_time DESC, seq DESC, event_id DESC
LIMIT $8
