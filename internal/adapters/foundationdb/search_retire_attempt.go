package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	retireAttemptSaveOperation = "save search session retirement attempt"
	// retireAttemptLease is how long recoverRetireAttempts treats a saved
	// attempt as active. It equals twice searchTransactionTimeout plus three
	// times audit.ExternalResultDeadline. These limits cover the attempt save,
	// cleanup, result write, failure code save, and recorder call. The configured
	// search worker lease cannot extend them. A clock difference between processes
	// can start recovery early. Recovery can then save an incomplete event while
	// the attempt owner can still save its failed event under a different event ID.
	retireAttemptLease = 2*searchTransactionTimeout + 3*audit.ExternalResultDeadline
)

// errRetireAttemptFenced is returned by beginCleanup, cleanupSlice, and
// clearRetireAttempt when the attempt record is absent. Recovery normally
// clears the record and saves an incomplete event. A clear that retries after
// its own commit applied can also find the record absent. This error does not
// wrap audit.ErrEventNotRecorded because the store did not fail to save
// evidence.
var errRetireAttemptFenced = errors.New("recovery already resolved this retirement attempt")

// errRetireRebuildEnded is returned by beginRetireAttempt when the replacement
// record is absent or retires another index. The attempt save reads the
// replacement key, and that read conflicts with FinishRebuild.
var errRetireRebuildEnded = errors.New("the index replacement of this retirement attempt has ended")

// errRetireAttemptsRemain is returned by FinishRebuild when an attempt record
// for the deleted index exists. The read conflicts with a late attempt save.
var errRetireAttemptsRemain = errors.New("retirement attempt records remain for the deleted index")

// retireAttemptRecord stores one attempt's lease deadline and encoded failure
// event.
type retireAttemptRecord struct {
	Deadline         time.Time       `json:"deadline"`
	Event            json.RawMessage `json:"event"`
	CleanupFailure   string          `json:"cleanup_failure,omitempty"`
	RecordingFailure string          `json:"recording_failure,omitempty"`
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
	encoded, err := json.Marshal(retireAttemptRecord{
		Deadline: s.work.clock.Now().Add(retireAttemptLease), Event: encodedEvent, CleanupFailure: "", RecordingFailure: "",
	})
	if err != nil {
		return retireAttempt{}, sessionFailure(ctx, retireAttemptSaveOperation, sessionID, err)
	}
	attempt := retireAttempt{key: fdb.Key(searchRetireAttemptKey(index, sessionID, step, event.EventID)), sessionID: sessionID, event: event}
	err = transactSession(ctx, s.db, s.work.clock, retireAttemptSaveOperation, sessionID, func(tr fdb.Transaction) error {
		rebuild, found, readErr := readRebuild(ctx, tr)
		if readErr != nil {
			return readErr
		}
		if !found || rebuild.SourceIndex != index {
			return errRetireRebuildEnded
		}
		tr.Set(attempt.key, encoded)
		return nil
	})
	if err != nil {
		return retireAttempt{}, err
	}
	slog.DebugContext(ctx, "search.session.retire_attempt_saved", slog.String("session_id", sessionID.String()), slog.String("step", string(step)))
	return attempt, nil
}
