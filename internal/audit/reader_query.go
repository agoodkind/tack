package audit

import (
	"fmt"

	"github.com/google/uuid"
)

// auditQueryPageDefault is how many rows a paged read returns when the caller
// names no limit. It bounds a page, never an export: the streaming export asks
// for zero, which means every row.
const auditQueryPageDefault = 100

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

// appendAuditQueryOrder finishes the statement. A positive limit reads the
// newest rows, so it orders and caps.
//
// A limit of zero asks for every matching row, which only the streaming export
// does, and it deliberately carries no ORDER BY. No index serves event_time
// order under an org-only filter: the ledger indexes event_time under an actor,
// an entity, or an action, and orders the primary key under a hashed
// (org_id, shard). Ordering an unbounded result therefore means a sort, and a
// sort must read and hold every matching row before it can emit the first one,
// which is the same "memory grows with the ledger" failure on the database side
// that streaming removes on the client side. Nothing downstream reads the file
// order: the verifier sorts what it scans into per-shard sequence order before
// it walks the chain.
//
// Exactly zero is what means every row. A negative limit is a caller mistake,
// not a request for the whole ledger, and Reader.Query normalises one to the
// page default, so reading it as unlimited here would make the same filter mean
// opposite things on the two paths: a page of a hundred rows one way, and an
// unbounded scan the other.
func appendAuditQueryOrder(query string, args []any, limit int) (string, []any) {
	if limit == 0 {
		return query, args
	}
	if limit < 0 {
		limit = auditQueryPageDefault
	}
	args = append(args, limit)
	return query + fmt.Sprintf(" ORDER BY event_time DESC, seq DESC LIMIT $%d", len(args)), args
}
