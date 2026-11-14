package audit

import (
	"fmt"

	"github.com/google/uuid"
)

func buildAuditQuery(filter QueryFilter) (string, []any) {
	query := `SELECT org_id, event_time, event_id, seq, shard,
	             actor_id, actor_kind, action, outcome, entity_kind, entity_id,
	             context, delta, error, extra, pii_ref, prev_hash, row_hash, hash_version,
	             idempotency_key
	      FROM audit.events
	      WHERE org_id = $1
	        AND event_time >= $2 AND event_time < $3`
	args := []any{filter.OrgID, filter.Oldest, filter.Latest}
	if filter.Action != "" {
		args = append(args, filter.Action)
		query += fmt.Sprintf(" AND action = $%d", len(args))
	}

	if filter.ActorID != uuid.Nil {
		args = append(args, filter.ActorID)
		query += fmt.Sprintf(" AND actor_id = $%d", len(args))
	}

	if filter.EntityID != uuid.Nil {
		args = append(args, filter.EntityID)
		query += fmt.Sprintf(" AND entity_id = $%d", len(args))
	}

	if filter.RequestID != "" {
		args = append(args, filter.RequestID)
		query += fmt.Sprintf(" AND context->>'request_id' = $%d", len(args))
	}

	if filter.TraceID != "" {
		args = append(args, filter.TraceID)
		query += fmt.Sprintf(" AND context->>'trace_id' = $%d", len(args))
	}

	if !filter.BeforeTime.IsZero() {
		args = append(args, filter.BeforeTime)
		timeParam := len(args)
		args = append(args, filter.BeforeSeq)
		query += fmt.Sprintf(" AND (event_time, seq) < ($%d, $%d)", timeParam, len(args))
	}

	return appendAuditQueryOrder(query, args, filter.Limit)
}

// appendAuditQueryOrder adds ordering and a row cap for a positive limit.
// A zero limit selects every matching row for a streaming export without
// ORDER BY. The ledger indexes event_time under an actor, entity, or action,
// and hashes the primary key by (org_id, shard). An org-only filter cannot
// use these indexes for event_time order. Sorting an unbounded result would
// require the database to read every matching row before returning the first.
// The verifier instead sorts exported rows by per-shard sequence before
// checking the chain.
//
// A negative limit uses DefaultQueryPageLimit, as Reader.Query does. Only
// zero requests an unbounded scan.
func appendAuditQueryOrder(query string, args []any, limit int) (string, []any) {
	if limit == 0 {
		return query, args
	}

	if limit < 0 {
		limit = DefaultQueryPageLimit
	}

	args = append(args, limit)

	return query + fmt.Sprintf(" ORDER BY event_time DESC, seq DESC LIMIT $%d", len(args)), args
}
