package audit

import (
	"fmt"

	"github.com/google/uuid"
)

func buildAuditQuery(filter QueryFilter) (string, []any) {
	query := `SELECT org_id, event_time, event_id, seq, shard,
	             actor_id, actor_kind, action, entity_kind, entity_id,
	             context, delta, idempotency_key
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
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY event_time DESC, seq DESC LIMIT $%d", len(args))
	return query, args
}
