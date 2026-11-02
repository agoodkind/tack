package foundationdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	retireAttemptInvalidCode    = "cleanup_attempt_invalid"
	retireAttemptInvalidMessage = "the search session cleanup attempt record could not be read"
	// retireAttemptRetainedName is the name used to derive the retained event ID.
	retireAttemptRetainedName = "retained"
	retireInvalidDigestField  = "invalid_attempt_sha256"
	retireInvalidRecordField  = "invalid_attempt_record"
)

// invalidRetireEvents returns two events for an unreadable attempt record.
// The first is an incomplete event that records the recovering operation on
// ctx, the system organization, and the session and step from the record key.
// The record does not supply the original identity, and the event does not
// record an original actor or time. Its evidence stores the SHA-256 digest of
// the stored value and omits the stored bytes. The ledger row for this event
// never stores those bytes.
// The second event does not specify an outcome and stores the bytes in its
// evidence. The audit consumer refuses this event and retains it in
// audit.events_dlq for repair.
func invalidRetireEvents(ctx context.Context, sessionID uuid.UUID, step searchdomain.CleanupStep, eventID uuid.UUID, stored []byte) (recoveredAttempt, error) {
	var none recoveredAttempt
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		return none, sessionStepError{operation: "read the retirement operation", err: err}
	}
	operation.Context.OrgID = uuid.Nil
	event, err := audit.ChangeFailureEvent(operation, audit.SearchSessionTarget(sessionID, uuid.Nil), cleanupVerb(step), audit.OutcomeDetails{
		Outcome: audit.OutcomeIncomplete, FailureCode: retireAttemptInvalidCode, FailureMessage: retireAttemptInvalidMessage, CommittedSteps: nil,
	})
	if err != nil {
		return none, sessionStepError{operation: "build the invalid attempt event", err: err}
	}
	retained := event
	retained.EventID = uuid.NewSHA1(eventID, []byte(retireAttemptRetainedName))
	retained.Outcome, retained.Error = "", nil
	if retained.Extra, err = addEvidenceField(operation.Extra, retireInvalidRecordField, stored); err != nil {
		return none, err
	}
	retainedJSON, err := audit.MarshalEvent(retained)
	if err != nil {
		return none, sessionStepError{operation: "encode the retained attempt event", err: err}
	}
	digest := sha256.Sum256(stored)
	event.EventID = uuid.NewSHA1(eventID, []byte(retireAttemptIncompleteName))
	if event.Extra, err = addEvidenceField(operation.Extra, retireInvalidDigestField, hex.EncodeToString(digest[:])); err != nil {
		return none, err
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return none, sessionStepError{operation: "encode the invalid attempt event", err: err}
	}
	return recoveredAttempt{active: false, encoded: encoded, retained: retainedJSON}, nil
}

// addEvidenceField returns extra with value added under name.
func addEvidenceField[Value string | []byte](extra json.RawMessage, name string, value Value) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(extra) != 0 {
		if err := json.Unmarshal(extra, &fields); err != nil {
			return nil, sessionStepError{operation: "decode the operation evidence", err: err}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, sessionStepError{operation: "encode the invalid attempt evidence", err: err}
	}
	fields[name] = encoded
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, sessionStepError{operation: "encode the operation evidence", err: err}
	}
	return merged, nil
}
