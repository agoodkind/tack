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

// appendAuditQueryOrder adds descending time and sequence ordering and a cap
// for positive limits. Exactly zero requests every matching row without
// ORDER BY, as the streaming export requires.
//
// The ledger's time indexes require an actor, entity, or action; its primary
// key hashes (org_id, shard). An organization-only time sort must process all
// matching rows before returning the first row. Omitting that sort avoids
// database-side work that grows with the matching set while the client
// streams rows. The verifier sorts exported rows by shard sequence before
// checking each chain and does not depend on the file's row order.
//
// A negative limit indicates a caller mistake and uses DefaultQueryPageLimit,
// matching Reader.Query. Treating a negative value as unlimited would return
// an unbounded stream for a filter that Query limits to a hundred rows.
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
