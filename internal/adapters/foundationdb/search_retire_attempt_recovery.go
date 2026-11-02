package foundationdb

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	retireAttemptRecoverOperation = "recover search session retirement attempts"
	// retireResultNotSavedCode identifies an incomplete event for an attempt that
	// did not save its result. The event does not establish whether the cleanup
	// step committed.
	retireResultNotSavedCode    = "cleanup_result_not_saved"
	retireResultNotSavedMessage = "the search session cleanup step did not save its result"
	retireAttemptIncompleteName = "incomplete"
)

// recoveredAttempt reports the recovery result for one attempt record. It
// reports whether the record is active and contains the encoded incomplete
// event. For an unreadable record, it also contains the retained event as JSON.
type recoveredAttempt struct {
	active   bool
	encoded  audit.EncodedEvent
	retained json.RawMessage
}

// recoverRetireAttempts reads up to limit attempt records for index in one
// transaction. For each record that is not active, it writes an incomplete
// event and clears the record. It reports whether an active record or more
// records can remain.
func (s *SearchRebuildStore) recoverRetireAttempts(ctx context.Context, index string, limit int) (bool, error) {
	keyRange, err := fdb.PrefixRange(searchRetireAttemptPrefix(index))
	if err != nil {
		return true, sessionFailure(ctx, retireAttemptRecoverOperation, uuid.Nil, err)
	}
	recovered, remain := 0, false
	err = transactSession(ctx, s.db, s.work.clock, retireAttemptRecoverOperation, uuid.Nil, func(tr fdb.Transaction) error {
		items, readErr := tr.GetRange(keyRange, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if readErr != nil {
			return sessionStepError{operation: "read retirement attempts", err: readErr}
		}
		recovered, remain = 0, len(items) == limit
		writer := NewOutboxWriter(tr)
		now := s.work.clock.Now()
		for _, item := range items {
			attempt, encodeErr := recoveredRetireEvent(ctx, item, now)
			if encodeErr != nil {
				return encodeErr
			}
			if attempt.active {
				remain = true
				continue
			}
			if writeErr := writer.WriteEncodedEvent(ctx, attempt.encoded); writeErr != nil {
				return writeErr
			}
			if attempt.retained != nil {
				if writeErr := writer.WriteEventJSON(ctx, attempt.retained); writeErr != nil {
					return writeErr
				}
			}
			tr.Clear(item.Key)
			recovered++
		}
		return nil
	})
	if err != nil {
		return true, err
	}
	if recovered > 0 {
		slog.InfoContext(ctx, "search.session.retire_attempts_recovered", slog.String("index", index), slog.Int("count", recovered))
	}
	return remain, nil
}

// recoveredRetireEvent returns the event for one attempt record or reports
// that the record is still active. A future deadline marks a record as active.
// If decoding, validation, or encoding fails, it logs the record and returns
// the events from invalidRetireEvents. An invalid record does not stop index
// retirement.
func recoveredRetireEvent(ctx context.Context, item fdb.KeyValue, now time.Time) (recoveredAttempt, error) {
	var attempt recoveredAttempt
	sessionID, step, eventID, err := parseRetireAttemptKey(item.Key)
	if err != nil {
		return attempt, sessionStepError{operation: "decode retirement attempt key", err: err}
	}
	var record retireAttemptRecord
	readErr := json.Unmarshal(item.Value, &record)
	if readErr == nil && record.Deadline.After(now) {
		attempt.active = true
		return attempt, nil
	}
	if readErr == nil {
		attempt.encoded, readErr = incompleteRetireEvent(record.Event, eventID, now)
	}
	if readErr == nil {
		return attempt, nil
	}
	slog.ErrorContext(ctx, "search.session.retire_attempt_invalid", slog.String("err", readErr.Error()), slog.String("session_id", sessionID.String()))
	return invalidRetireEvents(ctx, sessionID, step, eventID, item.Value)
}

// incompleteRetireEvent decodes an attempt event and returns an encoded event
// with an incomplete outcome and the retireResultNotSavedCode error. It retains
// the actor, operation ID, start time, and occurrence time and sets the
// completion time. It derives the new event ID from eventID. A late attempt
// owner can still save the failed event under the original event ID.
func incompleteRetireEvent(stored []byte, eventID uuid.UUID, now time.Time) (audit.EncodedEvent, error) {
	var event audit.Event
	if err := json.Unmarshal(stored, &event); err != nil {
		return audit.EncodedEvent{}, sessionStepError{operation: "decode retirement attempt event", err: err}
	}
	event.EventID = uuid.NewSHA1(eventID, []byte(retireAttemptIncompleteName))
	event.Outcome = audit.OutcomeIncomplete
	event.Error = &audit.EventError{Code: retireResultNotSavedCode, Message: retireResultNotSavedMessage}
	event.Context.CompletedAt = attemptCompletion(event, now)
	if err := audit.ValidateEvent(event); err != nil {
		return audit.EncodedEvent{}, sessionStepError{operation: "validate retirement attempt event", err: err}
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return audit.EncodedEvent{}, sessionStepError{operation: "encode retirement attempt event", err: err}
	}
	return encoded, nil
}
