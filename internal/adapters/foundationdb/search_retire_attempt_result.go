package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	retireAttemptFailureOperation = "record search session retirement failure"
	retireAttemptCodesOperation   = "save search session retirement failure codes"
	retireFailureUnclassified     = "unclassified"
	retireFailureDatabasePrefix   = "foundationdb_"
	retireFailureDeadline         = "context_deadline_exceeded"
	retireFailureCanceled         = "context_canceled"
	retireFailureUncertainSuffix  = "_commit_uncertain"
	retireRecorderMissing         = "the store has no failure recorder"
	// retireAttemptCodesName is the name used to derive the event ID for the
	// recorder event that contains the failure codes.
	retireAttemptCodesName = "failure-codes"
)

// recordRetireFailure clears the attempt record and writes the failure
// event to the outbox in one transaction. It ignores ctx cancellation and
// ends after audit.ExternalResultDeadline. It writes the failure event even
// if recovery already saved an incomplete event. The failure event records
// the definite result under a different event ID. A successful write returns
// cause unchanged. A failed write logs the recording error and returns an
// error containing cause and the recording error. That error matches
// audit.ErrEventNotRecorded if the failure code save succeeds or the recorder
// does not accept the event. It does not match audit.ErrEventNotRecorded if
// the failure code save does not save codes and the recorder accepts the
// event.
func (s *SearchRebuildStore) recordRetireFailure(ctx context.Context, attempt retireAttempt, step searchdomain.CleanupStep, cause error) error {
	sessionID := attempt.sessionID
	slog.ErrorContext(ctx, "search.session.cleanup_failed", slog.String("err", cause.Error()),
		slog.String("session_id", sessionID.String()), slog.String("step", string(step)))
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), audit.ExternalResultDeadline)
	defer cancel()
	result := retireResultEvent(attempt.event, s.work.clock.Now(), cause)
	encoded, err := audit.EncodeEvent(result, nil)
	if err == nil {
		err = transactSession(recordContext, s.db, s.work.clock, retireAttemptFailureOperation, sessionID, func(tr fdb.Transaction) error {
			// This read makes the failure event transaction conflict with a recovery
			// that clears the attempt record. The transaction writes the failure
			// event whether or not the record exists.
			if _, readErr := tr.Get(attempt.key).Get(); readErr != nil {
				return sessionStepError{operation: "read retirement attempt", err: readErr}
			}
			tr.Clear(attempt.key)
			if writeErr := NewOutboxWriter(tr).WriteEncodedEvent(recordContext, encoded); writeErr != nil {
				return writeErr
			}
			slog.DebugContext(ctx, "search.session.retire_failure_commit_ready", slog.String("session_id", sessionID.String()))
			return nil
		})
	}
	if err == nil {
		return cause
	}
	slog.ErrorContext(ctx, "search.session.audit_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
	joined := errors.Join(
		fmt.Errorf("search session %s cleanup step %s failed: %w", sessionID, step, cause),
		fmt.Errorf("record the failed cleanup step %s of search session %s: %w", step, sessionID, err),
	)
	if !s.saveRetireFailureCodes(ctx, attempt, cause, err) && s.recordRetireFailureCodes(ctx, attempt.sessionID, result, cause, err) {
		return joined
	}
	return errors.Join(audit.ErrEventNotRecorded, joined)
}

// recordRetireFailureCodes sends both failure codes to the recorder.
// It derives the event ID from the attempt event ID and
// retireAttemptCodesName. It uses the failed write time, computed once
// per failure. A write with an uncertain commit can also store an event
// without codes under the attempt event ID. The audit consumer's
// claimEventIdentity stores one row per event ID. The separate event ID
// permits both rows to remain. One attempt can have up to two failed rows
// under one operation: one without codes and one with both codes.
// The recorder call ignores ctx cancellation and ends after
// audit.ExternalResultDeadline. The function returns whether the recorder
// accepted the event. If the recorder is nil or fails, it logs both codes
// at Error.
func (s *SearchRebuildStore) recordRetireFailureCodes(ctx context.Context, attemptSessionID uuid.UUID, event audit.Event, cause, recordingErr error) bool {
	cleanupCode, recordingCode := retireFailureCode(cause), retireFailureCode(recordingErr)
	sessionID := attemptSessionID.String()
	if s.recorder == nil {
		slog.ErrorContext(ctx, "search.session.retire_codes_unrecorded", slog.String("err", retireRecorderMissing), slog.String("session_id", sessionID),
			slog.String("cleanup_code", cleanupCode), slog.String("recording_code", recordingCode))
		return false
	}
	extra, err := addEvidenceField(event.Extra, retireEvidenceCleanupField, cleanupCode)
	if err == nil {
		extra, err = addEvidenceField(extra, retireEvidenceRecordingField, recordingCode)
	}
	if err == nil {
		event.EventID = uuid.NewSHA1(event.EventID, []byte(retireAttemptCodesName))
		event.Extra = extra
		recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), audit.ExternalResultDeadline)
		defer cancel()
		err = s.recorder.Record(recordContext, event)
	}
	if err != nil {
		slog.ErrorContext(ctx, "search.session.retire_codes_unrecorded", slog.String("err", err.Error()), slog.String("session_id", sessionID),
			slog.String("cleanup_code", cleanupCode), slog.String("recording_code", recordingCode))
		return false
	}
	return true
}

// saveRetireFailureCodes saves the cleanup and recording failure codes in the
// attempt record in one transaction limited by searchTransactionTimeout. It
// stores codes without raw database error text. It returns true when it saves
// the codes. It returns false when the record does not exist or the transaction
// fails. It logs transaction errors.
func (s *SearchRebuildStore) saveRetireFailureCodes(ctx context.Context, attempt retireAttempt, cause, recordingErr error) bool {
	codesContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchTransactionTimeout)
	defer cancel()
	saved := false
	err := transactSession(codesContext, s.db, s.work.clock, retireAttemptCodesOperation, attempt.sessionID, func(tr fdb.Transaction) error {
		saved = false
		stored, readErr := tr.Get(attempt.key).Get()
		if readErr != nil {
			return sessionStepError{operation: "read retirement attempt", err: readErr}
		}
		if len(stored) == 0 {
			return nil
		}
		var record retireAttemptRecord
		if decodeErr := json.Unmarshal(stored, &record); decodeErr != nil {
			return sessionStepError{operation: "decode retirement attempt", err: decodeErr}
		}
		record.CleanupFailure, record.RecordingFailure = retireFailureCode(cause), retireFailureCode(recordingErr)
		encoded, encodeErr := json.Marshal(record)
		if encodeErr != nil {
			return sessionStepError{operation: "encode retirement attempt", err: encodeErr}
		}
		tr.Set(attempt.key, encoded)
		saved = true
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "search.session.retire_codes_failed", slog.String("err", err.Error()), slog.String("session_id", attempt.sessionID.String()))
		return false
	}
	return saved
}

// retireFailureCode returns a safe code for err: a FoundationDB error number,
// a context state, or unclassified. It adds a suffix when the commit could
// have applied.
func retireFailureCode(err error) string {
	code := retireFailureUnclassified
	var databaseError fdb.Error
	switch {
	case errors.As(err, &databaseError):
		code = retireFailureDatabasePrefix + strconv.Itoa(databaseError.Code)
	case errors.Is(err, context.DeadlineExceeded):
		code = retireFailureDeadline
	case errors.Is(err, context.Canceled):
		code = retireFailureCanceled
	}
	var uncertain commitUncertainError
	if errors.As(err, &uncertain) {
		return code + retireFailureUncertainSuffix
	}
	return code
}
