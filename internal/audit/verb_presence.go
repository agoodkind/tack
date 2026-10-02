package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// VerbPresence is the number of ledger rows of one verb in one organization
// and the newest event_time among them. Latest is nil when Rows is 0.
type VerbPresence struct {
	Verb   string
	Rows   int64
	Latest *time.Time
}

// VerbPresence reads, through the ledger reader, the row count and newest
// event_time of each verb in orgID. Every requested verb appears in the
// result, in the requested order, with Rows 0 when the ledger has none.
func (r *Reader) VerbPresence(ctx context.Context, orgID uuid.UUID, verbs []string) ([]VerbPresence, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("audit reader not configured")
	}
	rows, err := r.pool.Query(ctx, `SELECT action, count(event_id), max(event_time)
		FROM audit.events WHERE org_id = $1 AND action = ANY($2) GROUP BY action`, orgID, verbs)
	if err != nil {
		slog.ErrorContext(ctx, "audit.verb_presence.query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the presence of %d verbs: %w", len(verbs), err)
	}
	defer rows.Close()
	found := map[string]VerbPresence{}
	for rows.Next() {
		var presence VerbPresence
		if err := rows.Scan(&presence.Verb, &presence.Rows, &presence.Latest); err != nil {
			slog.ErrorContext(ctx, "audit.verb_presence.scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan a verb presence row: %w", err)
		}
		found[presence.Verb] = presence
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.verb_presence.rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the verb presence rows: %w", err)
	}
	result := make([]VerbPresence, 0, len(verbs))
	for _, verb := range verbs {
		presence, ok := found[verb]
		if !ok {
			presence = VerbPresence{Verb: verb, Rows: 0, Latest: nil}
		}
		result = append(result, presence)
	}
	return result, nil
}
