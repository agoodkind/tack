package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// PIIRefsForSubject lists audit.pii references for subjectID within orgID.
//
// The lookup includes references to redacted rows. It reads subject ownership
// directly from audit.pii rather than querying event actors.
//
//   - An unconfigured redactor returns an error before querying.
//   - Query, scan, and iteration failures return contextual errors.
func (r *Redactor) PIIRefsForSubject(
	ctx context.Context,
	orgID uuid.UUID,
	subjectID uuid.UUID,
) ([]uuid.UUID, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit redactor not configured")
	}

	rows, err := r.pool.Query(
		ctx,
		`
		SELECT pii_ref FROM audit.pii
		 WHERE org_id = $1 AND subject_id = $2
	`,
		orgID,
		subjectID,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.subject_refs_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf(
			"read audit.pii references for subject %s in org %s: %w",
			subjectID,
			orgID,
			err,
		)
	}
	defer rows.Close()
	refs := make([]uuid.UUID, 0)

	for rows.Next() {
		var ref uuid.UUID
		if err := rows.Scan(&ref); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.pii.subject_ref_scan_failed",
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf("scan audit.pii reference for subject %s: %w", subjectID, err)
		}

		refs = append(refs, ref)
	}

	if err := rows.Err(); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.pii.subject_refs_rows_failed",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("read audit.pii reference rows for subject %s: %w", subjectID, err)
	}

	return refs, nil
}
