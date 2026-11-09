package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// recordCleanupFailureOperation is the operation label of the failure event
	// transaction.
	recordCleanupFailureOperation = "record search session cleanup failure"
	// cleanupFailureMessage is the fixed message of a cleanup failure event.
	// The event does not store the raw database error.
	cleanupFailureMessage = "the search session cleanup step failed"
	// cleanupNotStartedCode and cleanupNotStartedMessage are the fixed failure
	// fields of a close or delete step that returned without a transaction
	// attempt.
	cleanupNotStartedCode    = "session_cleanup_not_started"
	cleanupNotStartedMessage = "the request deadline or cancellation ended the request before the cleanup step started"
)

// sessionNotStartedError marks a session transaction that returned without
// an attempt because its context had ended. It preserves the text of err.
type sessionNotStartedError struct{ err error }

func (e sessionNotStartedError) Error() string { return e.err.Error() }
func (e sessionNotStartedError) Unwrap() []error {
	return []error{e.err, searchdomain.ErrSessionStepNotStarted}
}

// RecordCleanupFailure commits one database change result event for step
// under the operation on ctx.
//
// It ignores cancellation of ctx and ends after
// [audit.ExternalResultDeadline]. The stored session supplies the
// organization and owner. An absent or undecodable header uses the system
// organization and omits the owner.
//
//   - A definite failure uses the failed outcome.
//   - A cause that wraps commitUncertainError uses the incomplete outcome
//     and cleanup_result_uncertain because the step could have committed.
func (s *SearchSessionStore) RecordCleanupFailure(ctx context.Context, sessionID uuid.UUID, step searchdomain.CleanupStep, cause error) error {
	slog.ErrorContext(ctx, "search.session.cleanup_failed", slog.String("err", cause.Error()),
		slog.String("session_id", sessionID.String()), slog.String("step", string(step)))
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), audit.ExternalResultDeadline)
	defer cancel()
	changes, err := beginSessionAudit(recordContext, recordCleanupFailureOperation, sessionID)
	if err != nil {
		return err
	}
	return transactSession(recordContext, s.db, s.clock, recordCleanupFailureOperation, sessionID, func(tr fdb.Transaction) error {
		changes.startAttempt()
		target := audit.SearchSessionTarget(sessionID, uuid.Nil)
		var record searchSessionRecord
		found, readErr := readSessionValue(tr, searchSessionKey(sessionID), &record)
		var databaseError fdb.Error
		if errors.As(readErr, &databaseError) {
			return readErr
		}
		if readErr == nil && found {
			changes.bind(record)
			target = audit.SearchSessionTarget(sessionID, record.PrincipalID)
		}
		return appendCleanupFailure(recordContext, tr, changes, target, step, cause)
	})
}

// appendCleanupFailure writes the failure event of step to the outbox in tr.
// Its event ID stays the same across transaction attempts.
func appendCleanupFailure(ctx context.Context, tr fdb.Transaction, changes *sessionAudit, target audit.Target, step searchdomain.CleanupStep, cause error) error {
	event, err := cleanupFailureEvent(ctx, changes, target, step, cause)
	if err != nil {
		return err
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		slog.ErrorContext(ctx, "search.session.audit_failed", slog.String("err", err.Error()), slog.String("session_id", target.ID.String()))
		return fmt.Errorf("encode the cleanup failure event of session %s: %w", target.ID, err)
	}
	return NewOutboxWriter(tr).WriteEncodedEvent(ctx, encoded)
}

// cleanupFailureEvent builds the result event for step. It sets the failed
// outcome, step as the code, and cleanupFailureMessage. When cause wraps
// commitUncertainError, it sets the incomplete outcome,
// retireResultUncertainCode, and retireResultUncertainMessage. When cause
// wraps sessionNotStartedError, it sets cleanupNotStartedCode and
// cleanupNotStartedMessage. The event ID derives from the operation base and
// step.
func cleanupFailureEvent(ctx context.Context, changes *sessionAudit, target audit.Target, step searchdomain.CleanupStep, cause error) (audit.Event, error) {
	details := audit.OutcomeDetails{
		Outcome: audit.OutcomeFailed, FailureCode: string(step), FailureMessage: cleanupFailureMessage, CommittedSteps: nil,
	}
	if errors.Is(cause, searchdomain.ErrSessionStepNotStarted) {
		details.FailureCode, details.FailureMessage = cleanupNotStartedCode, cleanupNotStartedMessage
	}
	var uncertain commitUncertainError
	if errors.As(cause, &uncertain) {
		details.Outcome, details.FailureCode, details.FailureMessage = audit.OutcomeIncomplete, retireResultUncertainCode, retireResultUncertainMessage
	}
	event, err := audit.ChangeFailureEvent(changes.operation, target, cleanupVerb(step), details)
	if err != nil {
		slog.ErrorContext(ctx, "search.session.audit_failed", slog.String("err", err.Error()), slog.String("session_id", target.ID.String()))
		return audit.Event{}, fmt.Errorf("build the cleanup failure event of session %s: %w", target.ID, err)
	}
	event.EventID = uuid.NewSHA1(changes.base, []byte("failure:"+string(step)))
	return event, nil
}

func cleanupVerb(step searchdomain.CleanupStep) audit.Verb {
	if step == searchdomain.CleanupClose {
		return audit.VerbSearchSessionUpdate
	}
	return audit.VerbSearchSessionDelete
}
