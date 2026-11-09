package audit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

//go:embed history_sql/search_session_header_deletes.sql
var searchSessionHeaderDeletesSQL string

// SearchSessionHeaderDeletes counts ledger rows in orgID that record a
// completed delete of the header record for sessionID. Each counted row
// has the search.session_delete action, completed outcome, change phase,
// and search_session target type. The count excludes failed or incomplete
// cleanup results and replay or visited removals. The function returns the
// count without row contents.
func (r *Reader) SearchSessionHeaderDeletes(
	ctx context.Context,
	orgID uuid.UUID,
	sessionID uuid.UUID,
) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("audit reader not configured")
	}

	var count int64
	err := r.pool.Scan(
		r.pool.unsanitized.QueryRow(
			ctx,
			searchSessionHeaderDeletesSQL,
			orgID,
			TargetKindResource,
			sessionID,
			string(VerbSearchSessionDelete),
			string(OutcomeCompleted),
			string(PhaseChange),
			TargetSearchSession,
		).Scan(
			&count,
		),
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.search_session_header_deletes.query_failed",
			slog.String("session_id", sessionID.String()),
			slog.String("err", err.Error()),
		)
		return 0, fmt.Errorf("count the header deletes of search session %s: %w", sessionID, err)
	}

	return count, nil
}
