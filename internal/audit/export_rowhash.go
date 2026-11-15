package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// legacyNanosecondCandidates covers the lost remainder in [0, 1000)
// nanoseconds.
// Versions 1 and 2 hashed producer nanoseconds but stored only microseconds.
// Trying every remainder recovers the original hash input (TACK-445).
const legacyNanosecondCandidates = 1000

// checkRowHash reproduces the stored hash and reports a mismatch reason.
// Version 3 uses stored precision with an additional explicit-zero-token-ID
// encoding attempt. Versions 1 and 2 try all lost nanosecond remainders.
func checkRowHash(row Row) (bool, string, error) {
	// Reject versions outside the range the ledger has written.
	if row.HashVersion < auditHashVersion1 || row.HashVersion > auditHashVersionCurrent {
		return false, fmt.Sprintf(
			"claims hash version %d, which the ledger never wrote",
			row.HashVersion,
		), nil
	}

	// Stored timestamps must use whole microseconds. An existing remainder
	// could let an edited version 3 timestamp reproduce its original instant
	// in the legacy candidate search after a false version downgrade.
	if row.HashVersion < auditHashVersion3 &&
		row.EventTime.Nanosecond()%legacyNanosecondCandidates != 0 {
		return false, fmt.Sprintf(
			"claims hash version %d with a sub-microsecond timestamp the ledger cannot store",
			row.HashVersion,
		), nil
	}

	// Validate the stored event contract without requiring protected payloads.
	// Reject contract violations even when the row hash matches.
	if reason := contractViolation(row); reason != "" {
		return false, reason, nil
	}

	if row.HashVersion < auditHashVersion3 {
		return checkLegacyRowHash(row)
	}

	expected, err := ComputeRowHash(row)
	if err != nil {
		return false, "", err
	}

	if bytesEqual(expected, row.RowHash) {
		return true, "", nil
	}

	return checkZeroTokenIDRowHash(row)
}

// contractViolation skips format 0 rows and returns the stored event
// validation failure for any other row. It returns an empty string when
// validation succeeds.
func contractViolation(row Row) string {
	if row.Context.AuditFormatVersion == AuditFormatLegacy {
		return ""
	}

	err := validateStoredEvent(exportEvent(row))
	if err == nil {
		return ""
	}

	return "event contract validation failed: " + err.Error()
}

// checkLegacyRowHash tries each possible nanosecond remainder for a version 1
// or 2 row.
func checkLegacyRowHash(row Row) (bool, string, error) {
	input, err := rowHashInputFor(row)
	if err != nil {
		return false, "", err
	}

	base := row.EventTime

	for remainder := range legacyNanosecondCandidates {
		input.Event.OccurredAt = base.Add(time.Duration(remainder) * time.Nanosecond)
		expected, err := hashRowForEvent(input)
		if err != nil {
			slog.Error(
				"audit.export.hash_failed",
				slog.String("event_id", row.EventID.String()),
				slog.String("err", err.Error()),
			)

			return false, "", fmt.Errorf("verify hash row %s: %w", row.EventID, err)
		}

		if bytesEqual(expected, row.RowHash) {
			return true, "", nil
		}
	}

	return false, "hash mismatch", nil
}

// ComputeRowHash computes the hash from stored fields and the stored version.
// Legacy verification additionally requires the lost timestamp remainder
// search.
func ComputeRowHash(row Row) ([]byte, error) {
	input, err := rowHashInputFor(row)
	if err != nil {
		return nil, err
	}

	hash, err := hashRowForEvent(input)
	if err != nil {
		slog.Error(
			"audit.export.hash_failed",
			slog.String("event_id", row.EventID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("verify hash row %s: %w", row.EventID, err)
	}

	return hash, nil
}

// rowHashInputFor reconstructs the writer's typed JSON payloads. Canonical
// hashing uses parsed values rather than JSON whitespace or key order.
func rowHashInputFor(row Row) (rowHashInput, error) {
	piiRef := uuid.Nil
	if row.PIIRef != nil {
		piiRef = *row.PIIRef
	}

	contextJSON, err := json.Marshal(row.Context)
	if err != nil {
		slog.Error(
			"audit.export.row_context_encode_failed",
			slog.String("event_id", row.EventID.String()),
			slog.String("err", err.Error()),
		)

		return rowHashInput{}, fmt.Errorf("verify row %s context: %w", row.EventID, err)
	}

	deltaJSON, err := json.Marshal(row.Delta)
	if err != nil {
		slog.Error(
			"audit.export.row_delta_encode_failed",
			slog.String("event_id", row.EventID.String()),
			slog.String("err", err.Error()),
		)

		return rowHashInput{}, fmt.Errorf("verify row %s delta: %w", row.EventID, err)
	}

	return rowHashInput{
		Event:       exportEvent(row),
		EventID:     row.EventID,
		Shard:       row.Shard,
		Seq:         row.Seq,
		PIIRef:      piiRef,
		ContextJSON: contextJSON,
		DeltaJSON:   deltaJSON,
		LastHash:    row.PrevHash,
		Version:     row.HashVersion,
	}, nil
}

// exportEvent copies the row's org column into the event hash input while
// rowHashInputFor separately encodes the original context JSON. Verification
// must detect changes to either org representation.
func exportEvent(row Row) Event {
	context := row.Context
	context.OrgID = row.OrgID

	return Event{
		Verb:    row.Action,
		EventID: row.EventID,
		Actor: Actor{
			Type:          actorTypeFromCode(row.ActorKind),
			ID:            row.ActorID,
			Email:         "",
			Name:          "",
			SessionID:     "",
			IP:            "",
			UserAgent:     "",
			RequestID:     "",
			APITokenLabel: "",
		},
		Entity: Entity{
			Type:       row.EntityKind,
			NodeType:   "",
			ID:         row.EntityID,
			Identifier: "",
			Name:       "",
		},
		Context:        context,
		Delta:          row.Delta,
		Outcome:        row.Outcome,
		Error:          row.Error,
		IdempotencyKey: row.IdempotencyKey,
		OccurredAt:     row.EventTime,
		Extra:          row.Extra,
	}
}

func actorTypeFromCode(code int16) ActorType {
	switch code {
	case 1:
		return ActorUser
	case 2:
		return ActorService
	case 3:
		return ActorSystem
	case 4:
		return ActorToken
	case 5:
		return ActorOperator
	case 6:
		return ActorUnauthenticated
	default:
		return ""
	}
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}
