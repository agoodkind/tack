package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// legacyNanosecondCandidates counts the possible sub-microsecond remainders in
// a version 1 or 2 row's original timestamp. Those versions hashed producer
// nanoseconds, but the database stores microseconds. The lost remainder lies in
// [0, 1000) nanoseconds. Trying every candidate restores content verification
// for legacy rows: the original timestamp reproduces an intact row's hash, and
// edited covered content does not match a candidate (TACK-445).
const legacyNanosecondCandidates = 1000

// checkRowHash reports whether the stored fields reproduce a row's stored hash
// and returns a reason for a mismatch. Version 3 uses stored timestamp
// precision and normally needs one computation. A zero token ID can require a
// second computation with an explicit context key. Versions 1 and 2 require a
// search over the possible lost nanosecond remainders.
func checkRowHash(row Row) (bool, string, error) {
	// The ledger has written only versions 1 through the current version. Reject a
	// different label as a forged version claim.
	if row.HashVersion < auditHashVersion1 || row.HashVersion > auditHashVersionCurrent {
		return false, fmt.Sprintf("claims hash version %d, which the ledger never wrote", row.HashVersion), nil
	}
	// The database stores whole microseconds. The legacy search adds a remainder to
	// that stored value. Accepting an existing remainder would let a forger
	// subtract one nanosecond from a version 3 timestamp and relabel the row as
	// legacy; the search could then recover the original instant and accept the
	// edit. Reject that unstoreable legacy timestamp.
	if row.HashVersion < auditHashVersion3 && row.EventTime.Nanosecond()%legacyNanosecondCandidates != 0 {
		return false, fmt.Sprintf(
			"claims hash version %d with a sub-microsecond timestamp the ledger cannot store",
			row.HashVersion), nil
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

// checkLegacyRowHash tries every nanosecond remainder that a version 1 or 2
// row's stored timestamp could have lost.
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
			slog.Error("audit.export.hash_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
			return false, "", fmt.Errorf("verify hash row %s: %w", row.EventID, err)
		}
		if bytesEqual(expected, row.RowHash) {
			return true, "", nil
		}
	}
	return false, "hash mismatch", nil
}

// ComputeRowHash computes a row's hash at its chain position from its own
// fields under its stored hash version, using the writer's hash operation.
//
// The verifier and substitute row sources use this function to reproduce
// writer hashing. Timestamp storage determines whether verification can
// compare the result directly.
//
// Verification distinguishes these cases:
//   - Current-version rows use their stored timestamp for direct comparison.
//   - Legacy rows require a candidate search to recover lost nanoseconds.
func ComputeRowHash(row Row) ([]byte, error) {
	input, err := rowHashInputFor(row)
	if err != nil {
		return nil, err
	}
	hash, err := hashRowForEvent(input)
	if err != nil {
		slog.Error("audit.export.hash_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify hash row %s: %w", row.EventID, err)
	}
	return hash, nil
}

// rowHashInputFor reconstructs hash input from a stored row. The writer
// marshaled the same types to JSON. Hash canonicalization compares parsed
// values, and marshaling the decoded values reproduces each covered key.
func rowHashInputFor(row Row) (rowHashInput, error) {
	piiRef := uuid.Nil
	if row.PIIRef != nil {
		piiRef = *row.PIIRef
	}
	contextJSON, err := json.Marshal(row.Context)
	if err != nil {
		slog.Error("audit.export.row_context_encode_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return rowHashInput{}, fmt.Errorf("verify row %s context: %w", row.EventID, err)
	}
	deltaJSON, err := json.Marshal(row.Delta)
	if err != nil {
		slog.Error("audit.export.row_delta_encode_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return rowHashInput{}, fmt.Errorf("verify row %s delta: %w", row.EventID, err)
	}
	return rowHashInput{
		Event:   exportEvent(row),
		EventID: row.EventID, Shard: row.Shard, Seq: row.Seq,
		PIIRef: piiRef, ContextJSON: contextJSON, DeltaJSON: deltaJSON,
		LastHash: row.PrevHash, Version: row.HashVersion,
	}, nil
}

// exportEvent reconstructs the event fields that hashing uses. The event hash
// reads Context.OrgID from the row's org_id column. rowHashInputFor separately
// marshals the original row.Context. An edit to either the column or the stored
// context changes its corresponding hash input.
func exportEvent(row Row) Event {
	context := row.Context
	context.OrgID = row.OrgID
	return Event{
		Verb: row.Action, EventID: row.EventID,
		Actor: Actor{
			Type: actorTypeFromCode(row.ActorKind), ID: row.ActorID,
			Email: "", Name: "", SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: Entity{
			Type: row.EntityKind, NodeType: "", ID: row.EntityID, Identifier: "", Name: "",
		},
		Context: context,
		Delta:   row.Delta, Outcome: row.Outcome, Error: row.Error,
		IdempotencyKey: row.IdempotencyKey, OccurredAt: row.EventTime, Extra: row.Extra,
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
