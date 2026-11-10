package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// PIIRefsForActor returns distinct audit.pii references from events for actorID
// within orgID.
//
// The reader role can SELECT audit.events, and the redactor role cannot. The
// reader performs this organization-scoped lookup; the redactor receives only
// the references that the caller supplies.
//
//   - Nil organization or actor IDs return an error.
//   - Events without a pii_ref do not contribute a reference.
func (r *Reader) PIIRefsForActor(
	ctx context.Context,
	orgID uuid.UUID,
	actorID uuid.UUID,
) ([]uuid.UUID, error) {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.pii_refs_for_actor",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("org.id", orgID.String()),
			attribute.String("audit.actor_id", actorID.String()),
		),
	)
	defer span.End()
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}

	if orgID == uuid.Nil {
		return nil, errors.New("audit pii refs: org_id required")
	}

	if actorID == uuid.Nil {
		return nil, errors.New("audit pii refs: actor_id required")
	}

	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			`
		SELECT DISTINCT pii_ref
		  FROM audit.events
		 WHERE org_id = $1 AND actor_id = $2 AND pii_ref IS NOT NULL
	`,
			orgID,
			actorID,
		),
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii_refs.query_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit pii refs for actor %s in org %s: %w", actorID, orgID, err)
	}
	defer rows.Close()
	var refs []uuid.UUID

	for rows.Next() {
		var ref uuid.UUID
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("audit pii ref scan: %w", err)
		}

		refs = append(refs, ref)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit pii refs rows: %w", err)
	}

	return refs, nil
}
