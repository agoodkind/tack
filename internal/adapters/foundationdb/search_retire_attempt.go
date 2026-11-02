package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	retireAttemptSaveOperation    = "save search session retirement attempt"
	retireAttemptFailureOperation = "record search session retirement failure"
	retireAttemptClearOperation   = "clear search session retirement attempt"
	// retireAttemptLease is the time recoverRetireAttempts treats a saved attempt
	// as active. It equals searchTransactionTimeout plus twice
	// audit.ExternalResultDeadline. retireStep and recordRetireFailure apply those
	// limits to the attempt save, cleanup step, and result write. The configured
	// search worker lease cannot extend those limits. A clock difference between
	// processes can start recovery early. Recovery can then save an incomplete
	// event while the attempt owner can still save its failed event under a
	// different event ID.
	retireAttemptLease = searchTransactionTimeout + 2*audit.ExternalResultDeadline
)

// errRetireAttemptFenced signals that beginCleanup or cleanupSlice found
// the attempt record absent after recovery cleared it and saved the
// attempt's incomplete event. The transaction does not commit a session
// change.
var errRetireAttemptFenced = errors.New("recovery already resolved this retirement attempt")

// retireAttemptRecord stores one attempt's lease deadline and encoded failure
// event.
type retireAttemptRecord struct {
	Deadline time.Time       `json:"deadline"`
	Event    json.RawMessage `json:"event"`
}

// retireAttempt identifies one saved retirement cleanup attempt by its
// FoundationDB record key and session ID and includes its failure event.
type retireAttempt struct {
	key       fdb.Key
	sessionID uuid.UUID
	event     audit.Event
}

// retireStep saves an attempt record within searchTransactionTimeout, runs
// effect within audit.ExternalResultDeadline, and resolves the record.
// If the save fails, retireStep returns the error without running effect.
// If effect returns errRetireAttemptFenced, retireStep returns that error
// without writing a failed event; recovery's incomplete event remains the
// evidence of the attempt. For another effect error, retireStep calls
// recordRetireFailure. If effect succeeds, retireStep calls
// clearRetireAttempt.
func (s *SearchRebuildStore) retireStep(
	ctx context.Context,
	index string,
	sessionID uuid.UUID,
	record searchSessionRecord,
	step searchdomain.CleanupStep,
	effect func(context.Context, fdb.Key) error,
) error {
	saveContext, cancelSave := context.WithTimeout(ctx, searchTransactionTimeout)
	defer cancelSave()
	attempt, err := s.beginRetireAttempt(saveContext, index, sessionID, record, step)
	if err != nil {
		return err
	}
	effectContext, cancelEffect := context.WithTimeout(ctx, audit.ExternalResultDeadline)
	defer cancelEffect()
	if err := effect(effectContext, attempt.key); err != nil {
		if errors.Is(err, errRetireAttemptFenced) {
			return err
		}
		return s.recordRetireFailure(ctx, attempt, step, err)
	}
	return s.clearRetireAttempt(ctx, attempt)
}

// requireRetireFence reads fence inside tr and returns
// errRetireAttemptFenced if the attempt record is absent. A nil fence skips
// the check for ordinary search cleanup. The read makes tr conflict with
// recovery when recovery clears the record.
func requireRetireFence(tr fdb.Transaction, fence fdb.Key) error {
	if fence == nil {
		return nil
	}
	stored, err := tr.Get(fence).Get()
	if err != nil {
		return sessionStepError{operation: "read retirement attempt", err: err}
	}
	if len(stored) == 0 {
		return errRetireAttemptFenced
	}
	return nil
}

// beginRetireAttempt builds the step's failure event from the audit operation
// on ctx and the session record. Before the cleanup step runs, it saves the
// event under the index with the original actor, operation ID, and start time.
func (s *SearchRebuildStore) beginRetireAttempt(ctx context.Context, index string, sessionID uuid.UUID, record searchSessionRecord, step searchdomain.CleanupStep) (retireAttempt, error) {
	changes, err := beginSessionAudit(ctx, retireAttemptSaveOperation, sessionID)
	if err != nil {
		return retireAttempt{}, err
	}
	changes.bind(record)
	event, err := cleanupFailureEvent(ctx, changes, audit.SearchSessionTarget(sessionID, record.PrincipalID), step)
	if err != nil {
		return retireAttempt{}, err
	}
	encodedEvent, err := audit.MarshalEvent(event)
	if err != nil {
		return retireAttempt{}, sessionFailure(ctx, retireAttemptSaveOperation, sessionID, err)
	}
	encoded, err := json.Marshal(retireAttemptRecord{Deadline: s.work.clock.Now().Add(retireAttemptLease), Event: encodedEvent})
	if err != nil {
		return retireAttempt{}, sessionFailure(ctx, retireAttemptSaveOperation, sessionID, err)
	}
	attempt := retireAttempt{key: fdb.Key(searchRetireAttemptKey(index, sessionID, step, event.EventID)), sessionID: sessionID, event: event}
	err = transactSession(ctx, s.db, s.work.clock, retireAttemptSaveOperation, sessionID, func(tr fdb.Transaction) error {
		tr.Set(attempt.key, encoded)
		return nil
	})
	if err != nil {
		return retireAttempt{}, err
	}
	slog.DebugContext(ctx, "search.session.retire_attempt_saved", slog.String("session_id", sessionID.String()), slog.String("step", string(step)))
	return attempt, nil
}

// recordRetireFailure clears the attempt record and writes the failure event
// to the outbox in one transaction. It ignores cancellation of ctx and ends
// after audit.ExternalResultDeadline. It writes the failure event even if
// recovery already saved an incomplete event. The failure event records the
// definite result under a different event ID. If recording succeeds, it returns
// cause unchanged. If recording fails, it logs the recording error, retains the
// attempt record for recoverRetireAttempts, and returns an error joining cause
// and the recording error.
func (s *SearchRebuildStore) recordRetireFailure(ctx context.Context, attempt retireAttempt, step searchdomain.CleanupStep, cause error) error {
	sessionID := attempt.sessionID
	slog.ErrorContext(ctx, "search.session.cleanup_failed", slog.String("err", cause.Error()),
		slog.String("session_id", sessionID.String()), slog.String("step", string(step)))
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), audit.ExternalResultDeadline)
	defer cancel()
	event := attempt.event
	now := attemptCompletion(event, s.work.clock.Now())
	event.OccurredAt, event.Context.CompletedAt = now, now
	encoded, err := audit.EncodeEvent(event, nil)
	if err == nil {
		err = transactSession(recordContext, s.db, s.work.clock, retireAttemptFailureOperation, sessionID, func(tr fdb.Transaction) error {
			// This read makes the failure event transaction conflict with a recovery
			// that clears the attempt record. The transaction writes the failure
			// event whether or not the record exists.
			if _, readErr := tr.Get(attempt.key).Get(); readErr != nil {
				return sessionStepError{operation: "read retirement attempt", err: readErr}
			}
			tr.Clear(attempt.key)
			return NewOutboxWriter(tr).WriteEncodedEvent(recordContext, encoded)
		})
	}
	if err == nil {
		return cause
	}
	slog.ErrorContext(ctx, "search.session.audit_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
	return errors.Join(
		fmt.Errorf("search session %s cleanup step %s failed: %w", sessionID, step, cause),
		fmt.Errorf("record the failed cleanup step %s of search session %s: %w", step, sessionID, err),
	)
}

// clearRetireAttempt removes the attempt record after the cleanup step commits.
// The cleanup step commits its change events in its own transaction. If the
// clear fails, recoverRetireAttempts later saves an incomplete event for the
// attempt.
func (s *SearchRebuildStore) clearRetireAttempt(ctx context.Context, attempt retireAttempt) error {
	clearContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), audit.ExternalResultDeadline)
	defer cancel()
	return transactSession(clearContext, s.db, s.work.clock, retireAttemptClearOperation, attempt.sessionID, func(tr fdb.Transaction) error {
		tr.Clear(attempt.key)
		return nil
	})
}

func attemptCompletion(event audit.Event, now time.Time) time.Time {
	completed := now.UTC()
	if completed.Before(event.Context.StartedAt) {
		return event.Context.StartedAt
	}
	return completed
}
