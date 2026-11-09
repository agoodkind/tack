package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	// recordWriteUncertainOperation is the operation label for the incomplete
	// session write event transaction.
	recordWriteUncertainOperation = "record search session write uncertain"
	// sessionWriteUncertainCode is the failure code for an event that records a
	// session write without a confirmed commit result.
	sessionWriteUncertainCode = "session_write_uncertain"
	// sessionWriteUncertainMessage is the fixed message for an incomplete session
	// write event. The event does not store the raw database error.
	sessionWriteUncertainMessage = "the search session write ended without a confirmed commit result"
	// sessionWriteUncertainEventName is the name used to derive the event ID from
	// the operation base.
	sessionWriteUncertainEventName = "write_uncertain"
)

// recordWriteUncertain commits an incomplete database change result event
// in a separate transaction when cause wraps commitUncertainError.
// The event uses the operation and organization from changes, verb, and the
// session target with ownerID. Its fixed code and message exclude the raw
// error. Its ID derives from the operation base in changes, so repeated
// writes use the same event ID.
// recordWriteUncertain ignores cancellation of ctx.
// audit.ExternalResultDeadline limits the whole write to 30 seconds.
// searchTransactionTimeout limits each transaction attempt to 5 seconds.
// transactSession does not retry FoundationDB timeout error 1031.
// One stalled attempt ends the write before audit.ExternalResultDeadline.
// It returns cause when the event commits or
// when cause does not wrap commitUncertainError. If the event cannot commit,
// it returns an error joining audit.ErrEventNotRecorded, cause, and the write
// error.
func (s *SearchSessionStore) recordWriteUncertain(
	ctx context.Context,
	changes *sessionAudit,
	sessionID uuid.UUID,
	ownerID uuid.UUID,
	verb audit.Verb,
	cause error,
) error {
	var uncertain commitUncertainError
	if !errors.As(cause, &uncertain) {
		return cause
	}

	recordContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		audit.ExternalResultDeadline,
	)
	defer cancel()
	err := transactSession(
		recordContext,
		s.db,
		s.clock,
		recordWriteUncertainOperation,
		sessionID,
		func(tr fdb.Transaction) error {
			return appendWriteUncertain(recordContext, tr, changes, sessionID, ownerID, verb)
		},
	)
	if err != nil {
		joined := errors.Join(cause, err)
		return sessionRecordingFailure(ctx, sessionID, joined)
	}

	return cause
}

// appendWriteUncertain writes an incomplete session write event for the session
// target with ownerID to the outbox in tr. The event ID stays the same across
// transaction attempts.
func appendWriteUncertain(
	ctx context.Context,
	tr fdb.Transaction,
	changes *sessionAudit,
	sessionID uuid.UUID,
	ownerID uuid.UUID,
	verb audit.Verb,
) error {
	details := audit.OutcomeDetails{
		Outcome:        audit.OutcomeIncomplete,
		FailureCode:    sessionWriteUncertainCode,
		FailureMessage: sessionWriteUncertainMessage,
		CommittedSteps: nil,
	}
	event, err := audit.ChangeFailureEvent(
		changes.operation,
		audit.SearchSessionTarget(sessionID, ownerID),
		verb,
		details,
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"search.session.audit_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", sessionID.String()),
		)

		return fmt.Errorf("build the uncertain write event of session %s: %w", sessionID, err)
	}

	event.EventID = uuid.NewSHA1(changes.base, []byte(sessionWriteUncertainEventName))
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"search.session.audit_failed",
			slog.String("err", err.Error()),
			slog.String("session_id", sessionID.String()),
		)

		return fmt.Errorf("encode the uncertain write event of session %s: %w", sessionID, err)
	}

	return NewOutboxWriter(tr).WriteEncodedEvent(ctx, encoded)
}
