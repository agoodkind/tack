-- The query reads one page of a node's ledger rows in one org, newest first.
--
-- The parameters select the rows and page boundary:
--   - $1 supplies org_id.
--   - $2 supplies the node ID as a UUID, and $3 supplies it as text.
--   - $4 supplies the inclusive oldest event_time.
--   - $5, $6, and $7 supply the exclusive (event_time, seq, event_id) boundary.
--   - $8 supplies the page size.
--
-- Each branch reads one index and stops at the page size. The first branch
-- reads the node's own rows through events_entity_time. The second branch
-- uses that index for legacy relationship rows with the source in entity_id
-- and 'relationship' in entity_kind. Format 2 relationship rows store the
-- zero UUID in entity_id, and the second branch excludes them. The third and
-- fourth branches read format 2 rows through events_relationship_source_time
-- and events_relationship_target_time. The fourth branch excludes
-- self-relationships that the third branch already returns.
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
