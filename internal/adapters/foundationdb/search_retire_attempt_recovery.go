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
	retireAttemptRecoverOperation = "recover search session retirement attempts"
	// retireResultNotSavedCode identifies an incomplete event for an attempt that
	// did not save its result. The event does not establish whether the cleanup
	// step committed.
	retireResultNotSavedCode     = "cleanup_result_not_saved"
	retireResultNotSavedMessage  = "the search session cleanup step did not save its result"
	retireAttemptIncompleteName  = "incomplete"
	retireResultUncertainCode    = "cleanup_result_uncertain"
	retireResultUncertainMessage = "the search session cleanup step ended without a confirmed commit result"
	retireEvidenceCleanupField   = "cleanup_failure_code"
	retireEvidenceRecordingField = "recording_failure_code"
	// retireRecoveryValueBytes limits the attempt record bytes one recovery
	// transaction processes before it stops and reports remaining work. This
	// limit keeps the written events below FoundationDB's transaction limit of
	// 10,000,000 bytes.
	retireRecoveryValueBytes = 1 << 20
)

// recoveredAttempt reports the recovery result for one attempt record.
// It reports whether the record is active and contains the value to store
// for a first observation. For an expired record, it contains the encoded
// incomplete event. For an unreadable record, it also contains the retained
// events as JSON.
type recoveredAttempt struct {
	active   bool
	observed []byte
	encoded  audit.EncodedEvent
	retained []json.RawMessage
}

// recoverRetireAttempts reads up to limit attempt records for index in one
// transaction. For each record that is not active, it writes an incomplete
// event and clears the record. It reports whether an active record or more
// records can remain.
func (s *SearchRebuildStore) recoverRetireAttempts(
	ctx context.Context,
	index string,
	limit int,
) (bool, error) {
	keyRange, err := fdb.PrefixRange(searchRetireAttemptPrefix(index))
	if err != nil {
		return true, sessionFailure(ctx, retireAttemptRecoverOperation, uuid.Nil, err)
	}

	recovered, remain := 0, false
	err = transactSession(
		ctx,
		s.db,
		s.work.clock,
		retireAttemptRecoverOperation,
		uuid.Nil,
		func(tr fdb.Transaction) error {
			items, readErr := tr.GetRange(
				keyRange,
				fdb.RangeOptions{Limit: limit},
			).GetSliceWithError()
			if readErr != nil {
				return sessionStepError{operation: "read retirement attempts", err: readErr}
			}

			recovered, remain = 0, len(items) == limit
			writer := NewOutboxWriter(tr)
			now := s.work.clock.Now()
			valueBytes := 0

			for _, item := range items {
				if valueBytes >= retireRecoveryValueBytes {
					remain = true
					break
				}

				attempt, encodeErr := recoveredRetireEvent(ctx, item, now)
				if encodeErr != nil {
					return encodeErr
				}

				if attempt.active {
					remain = true
					if attempt.observed != nil {
						tr.Set(item.Key, attempt.observed)
						valueBytes += len(attempt.observed)
					}

					continue
				}

				valueBytes += len(item.Value)
				if writeErr := writer.WriteEncodedEvent(ctx, attempt.encoded); writeErr != nil {
					return writeErr
				}

				for _, retained := range attempt.retained {
					if writeErr := writer.WriteEventJSON(ctx, retained); writeErr != nil {
						return writeErr
					}
				}

				tr.Clear(item.Key)
				recovered++
			}

			return nil
		},
	)
	if err != nil {
		return true, err
	}

	if recovered > 0 {
		slog.InfoContext(
			ctx,
			"search.session.retire_attempts_recovered",
			slog.String("index", index),
			slog.Int("count", recovered),
		)
	}

	return remain, nil
}

// recoveredRetireEvent returns the event for one attempt record or reports
// that the record is still active. observeRetireAttempt decides whether
// the record is active. If decoding, validation, or encoding fails, the
// function logs the record and returns the events from invalidRetireEvents.
// An invalid record does not stop index retirement.
func recoveredRetireEvent(
	ctx context.Context,
	item fdb.KeyValue,
	now time.Time,
) (recoveredAttempt, error) {
	var attempt recoveredAttempt
	sessionID, step, eventID, err := parseRetireAttemptKey(item.Key)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"search.session.retire_attempt_invalid",
			slog.String("err", err.Error()),
			slog.String("session_id", sessionID.String()),
		)

		return invalidRetireEvents(
			ctx,
			uuid.Nil,
			searchdomain.CleanupClose,
			uuid.NewSHA1(uuid.NameSpaceOID, item.Key),
			item,
		)
	}

	var record retireAttemptRecord
	var event audit.Event
	readErr := json.Unmarshal(item.Value, &record)
	if readErr == nil {
		readErr = json.Unmarshal(record.Event, &event)
	}

	if readErr == nil {
		attempt.active, attempt.observed, readErr = observeRetireAttempt(record, now)
	}

	if readErr == nil && attempt.active {
		return attempt, nil
	}

	if readErr == nil {
		attempt.encoded, readErr = incompleteRetireEvent(event, record, eventID, now)
	}

	if readErr == nil {
		return attempt, nil
	}

	slog.ErrorContext(
		ctx,
		"search.session.retire_attempt_invalid",
		slog.String("err", readErr.Error()),
		slog.String("session_id", sessionID.String()),
	)

	return invalidRetireEvents(ctx, sessionID, step, eventID, item)
}

// retireResultEvent sets the occurrence and completion times of an attempt
// failure event. When cause wraps commitUncertainError, it sets the incomplete
// outcome and retireResultUncertainCode because the cleanup step could have
// committed. A failed outcome means the step did not commit.
func retireResultEvent(event audit.Event, now time.Time, cause error) audit.Event {
	completed := attemptCompletion(event, now)
	event.OccurredAt, event.Context.CompletedAt = completed, completed
	var uncertain commitUncertainError
	if errors.As(cause, &uncertain) {
		event.Outcome = audit.OutcomeIncomplete
		event.Error = &audit.EventError{
			Code:    retireResultUncertainCode,
			Message: retireResultUncertainMessage,
		}
	}

	return event
}

func attemptCompletion(event audit.Event, now time.Time) time.Time {
	completed := now.UTC()
	if completed.Before(event.Context.StartedAt) {
		return event.Context.StartedAt
	}

	return completed
}

// incompleteRetireEvent decodes an attempt event and returns an encoded event
// with an incomplete outcome and the retireResultNotSavedCode error. It retains
// the actor, operation ID, start time, and occurrence time and sets the
// completion time. It derives the new event ID from eventID. A late attempt
// owner can still save the failed event under the original event ID.
func incompleteRetireEvent(
	event audit.Event,
	record retireAttemptRecord,
	eventID uuid.UUID,
	now time.Time,
) (audit.EncodedEvent, error) {
	event.EventID = uuid.NewSHA1(eventID, []byte(retireAttemptIncompleteName))
	event.Outcome = audit.OutcomeIncomplete
	event.Error = &audit.EventError{
		Code:    retireResultNotSavedCode,
		Message: retireResultNotSavedMessage,
	}
	event.Context.CompletedAt = attemptCompletion(event, now)
	var err error
	if record.CleanupFailure != "" {
		if event.Extra, err = addEvidenceField(
			event.Extra,
			retireEvidenceCleanupField,
			record.CleanupFailure,
		); err != nil {
			return audit.EncodedEvent{}, err
		}
	}

	if record.RecordingFailure != "" {
		if event.Extra, err = addEvidenceField(
			event.Extra,
			retireEvidenceRecordingField,
			record.RecordingFailure,
		); err != nil {
			return audit.EncodedEvent{}, err
		}
	}

	if err := audit.ValidateEvent(event); err != nil {
		return audit.EncodedEvent{}, sessionStepError{
			operation: "validate retirement attempt event",
			err:       err,
		}
	}

	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return audit.EncodedEvent{}, sessionStepError{
			operation: "encode retirement attempt event",
			err:       err,
		}
	}

	return encoded, nil
}
