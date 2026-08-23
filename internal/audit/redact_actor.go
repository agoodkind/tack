package audit

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ActorRedaction reports one org-scoped actor redaction, planned or applied.
type ActorRedaction struct {
	OrgID   uuid.UUID
	ActorID uuid.UUID
	// PIIRefCount is how many audit.pii rows events in the org reference for
	// the actor, redacted or not.
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

// RedactActorInOrg erases the PII payload behind every event that orgID
// recorded for actorID. The ledger rows stay and the hash chain stays valid:
// the chain covers the pii reference, not the payload. Events the same actor
// left in any other org are untouched.
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
	refs, err := reader.PIIRefsForActor(ctx, orgID, actorID)
	if err != nil {
		return ActorRedaction{}, err
	}
	result.PIIRefCount = len(refs)
	result.Unredacted, err = redactor.CountUnredacted(ctx, refs)
	if err != nil {
		return ActorRedaction{}, err
	}
	if !apply {
		return result, nil
	}
	result.Redacted, err = redactor.RedactPIIRefs(ctx, refs)
	if err != nil {
		return ActorRedaction{}, err
	}
	return result, nil
}
