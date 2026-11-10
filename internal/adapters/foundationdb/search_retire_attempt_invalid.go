package foundationdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	retireAttemptInvalidCode    = "cleanup_attempt_invalid"
	retireAttemptInvalidMessage = "the search session cleanup attempt record could not be read"
	// retireAttemptRetainedName supplies the prefix for deriving a retained
	// event ID.
	retireAttemptRetainedName = "retained:"
	retireInvalidDigestField  = "invalid_attempt_sha256"
	retireInvalidRecordField  = "invalid_attempt_record"
	retireInvalidKeyField     = "invalid_attempt_key"
	retireInvalidIndexField   = "invalid_attempt_chunk_index"
	retireInvalidCountField   = "invalid_attempt_chunk_count"
	retireInvalidChunkBytes   = 48 << 10
)

// invalidRetireEvents returns events for an unreadable attempt record. The
// first event records an incomplete outcome, the recovering operation on
// ctx, the system organization, and the session and step from the record
// key. The record does not supply the original identity, and the event does
// not record an original actor or time. Its evidence stores the SHA-256
// digest of the stored value and omits the stored bytes. The remaining
// events omit an outcome and store the key and value bytes in chunks. The
// audit consumer refuses those events and retains them in audit.events_dlq
// for repair.
func invalidRetireEvents(
	ctx context.Context,
	sessionID uuid.UUID,
	step searchdomain.CleanupStep,
	eventID uuid.UUID,
	item fdb.KeyValue,
) (recoveredAttempt, error) {
	var none recoveredAttempt
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		return none, sessionStepError{operation: "read the retirement operation", err: err}
	}

	operation.Context.OrgID = uuid.Nil
	event, err := audit.ChangeFailureEvent(
		operation,
		audit.SearchSessionTarget(sessionID, uuid.Nil),
		cleanupVerb(step),
		audit.OutcomeDetails{
			Outcome:        audit.OutcomeIncomplete,
			FailureCode:    retireAttemptInvalidCode,
			FailureMessage: retireAttemptInvalidMessage,
			CommittedSteps: nil,
		},
	)
	if err != nil {
		return none, sessionStepError{operation: "build the invalid attempt event", err: err}
	}

	digest := sha256.Sum256(item.Value)
	digestText := hex.EncodeToString(digest[:])
	retained, err := retainedAttemptEvents(event, operation.Extra, eventID, digestText, item)
	if err != nil {
		return none, err
	}

	event.EventID = uuid.NewSHA1(eventID, []byte(retireAttemptIncompleteName))
	if event.Extra, err = addEvidenceField(
		operation.Extra,
		retireInvalidDigestField,
		digestText,
	); err != nil {
		return none, err
	}

	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return none, sessionStepError{operation: "encode the invalid attempt event", err: err}
	}

	return recoveredAttempt{active: false, observed: nil, encoded: encoded, retained: retained}, nil
}

// retainedAttemptEvents splits the stored value into chunks and returns an
// event for each chunk. Each event stays below FoundationDB's 100,000-byte
// value limit after base64 encoding. Each event records the chunk index,
// chunk count, SHA-256 digest of the whole value, and record key. Each event
// ID derives from eventID and the chunk index.
func retainedAttemptEvents(
	event audit.Event,
	extra json.RawMessage,
	eventID uuid.UUID,
	digest string,
	item fdb.KeyValue,
) ([]json.RawMessage, error) {
	chunkCount := max(1, (len(item.Value)+retireInvalidChunkBytes-1)/retireInvalidChunkBytes)

	retained := make([]json.RawMessage, 0, chunkCount)
	for chunkIndex := range chunkCount {
		start := chunkIndex * retireInvalidChunkBytes
		chunk := item.Value[start:min(start+retireInvalidChunkBytes, len(item.Value))]
		fields, err := addEvidenceField(extra, retireInvalidRecordField, chunk)
		if err == nil {
			fields, err = addEvidenceField(fields, retireInvalidKeyField, []byte(item.Key))
		}

		if err == nil {
			fields, err = addEvidenceField(fields, retireInvalidDigestField, digest)
		}

		if err == nil {
			fields, err = addEvidenceField(fields, retireInvalidIndexField, chunkIndex)
		}

		if err == nil {
			fields, err = addEvidenceField(fields, retireInvalidCountField, chunkCount)
		}

		if err != nil {
			return nil, err
		}

		chunkEvent := event
		chunkEvent.EventID = uuid.NewSHA1(
			eventID,
			[]byte(retireAttemptRetainedName+strconv.Itoa(chunkIndex)),
		)
		chunkEvent.Outcome, chunkEvent.Error, chunkEvent.Extra = "", nil, fields
		encoded, err := audit.MarshalEvent(chunkEvent)
		if err != nil {
			return nil, sessionStepError{operation: "encode the retained attempt event", err: err}
		}

		retained = append(retained, encoded)
	}

	return retained, nil
}

// addEvidenceField returns extra with value added under name.
func addEvidenceField[Value string | []byte | int](
	extra json.RawMessage,
	name string,
	value Value,
) (json.RawMessage, error) {
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
