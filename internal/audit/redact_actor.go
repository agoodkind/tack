package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ActorRedaction reports one org-scoped actor redaction, planned or applied.
type ActorRedaction struct {
	OrgID   uuid.UUID
	ActorID uuid.UUID
	// PIIRefCount counts distinct audit.pii references for the actor's events
	// and protected values for the actor as subject within the org, including
	// references to redacted rows.
	PIIRefCount int
	// Unredacted is how many of those rows still held a payload before this
	// run.
	Unredacted int64
	// Redacted is how many rows this run erased. A plan reports zero.
	Redacted int64
}

// PlanActorRedaction reports what RedactActorInOrg would erase and changes
// nothing.
func PlanActorRedaction(ctx context.Context, reader *Reader, redactor *Redactor, orgID, actorID uuid.UUID) (ActorRedaction, error) {
	return redactActorInOrg(ctx, reader, redactor, orgID, actorID, false)
}

// RedactActorInOrg erases audit.pii payloads for actorID as an event actor or
// protected value subject within orgID. The hash chain covers references
// rather than payloads and remains valid after redaction.
// Redaction does not erase copies in audit.events_dlq,
// audit.event_assembly_parts, Kafka, or outbox rows.
func RedactActorInOrg(ctx context.Context, reader *Reader, redactor *Redactor, orgID, actorID uuid.UUID) (ActorRedaction, error) {
	return redactActorInOrg(ctx, reader, redactor, orgID, actorID, true)
}

func redactActorInOrg(ctx context.Context, reader *Reader, redactor *Redactor, orgID, actorID uuid.UUID, apply bool) (ActorRedaction, error) {
	if reader == nil {
		return ActorRedaction{}, errors.New("audit redact actor: reader required")
	}
	if redactor == nil {
		return ActorRedaction{}, errors.New("audit redact actor: redactor required")
	}
	result := ActorRedaction{OrgID: orgID, ActorID: actorID, PIIRefCount: 0, Unredacted: 0, Redacted: 0}
	actorRefs, err := reader.PIIRefsForActor(ctx, orgID, actorID)
	if err != nil {
		return ActorRedaction{}, redactActorError(ctx, orgID, actorID, err)
	}
	subjectRefs, err := redactor.PIIRefsForSubject(ctx, orgID, actorID)
	if err != nil {
		return ActorRedaction{}, redactActorError(ctx, orgID, actorID, err)
	}
	refs := uniqueRefs(actorRefs, subjectRefs)
	result.PIIRefCount = len(refs)
	result.Unredacted, err = redactor.CountUnredacted(ctx, refs)
	if err != nil {
		return ActorRedaction{}, redactActorError(ctx, orgID, actorID, err)
	}
	if !apply {
		return result, nil
	}
	result.Redacted, err = redactor.RedactPIIRefs(ctx, refs)
	if err != nil {
		return ActorRedaction{}, redactActorError(ctx, orgID, actorID, err)
	}
	return result, nil
}

// uniqueRefs merges reference groups without duplicates and preserves the
// order of each reference's first occurrence.
func uniqueRefs(groups ...[]uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	refs := make([]uuid.UUID, 0)
	for _, group := range groups {
		for _, ref := range group {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

// redactActorError stamps the failing step with the identifiers this layer
// alone holds together.
func redactActorError(ctx context.Context, orgID, actorID uuid.UUID, err error) error {
	slog.ErrorContext(ctx, "audit.redact_actor.failed",
		slog.String("org_id", orgID.String()),
		slog.String("actor_id", actorID.String()),
		slog.String("err", err.Error()),
	)
	return fmt.Errorf("redact actor %s in org %s: %w", actorID, orgID, err)
}
