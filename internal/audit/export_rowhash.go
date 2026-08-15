package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// legacyNanosecondCandidates is how many sub-microsecond values a version 1
// or 2 row's hash can have covered. Those versions hashed the producer's
// nanosecond timestamp while the column stores microseconds, so the only
// unrecoverable input is the remainder in [0, 1000) nanoseconds. Trying every
// candidate restores full content verification for the whole ledger: a row
// matches under exactly the timestamp it was written with, and an edited row
// matches under none, whatever version it claims (TACK-445).
const legacyNanosecondCandidates = 1000

// checkRowHash reports whether the stored row hash is reproducible from the
// stored row, and names the failure when it is not. Version 3 rows recompute
// in one try because their hash covers the timestamp at stored precision.
// Version 1 and 2 rows are tried at every possible lost nanosecond remainder.
func checkRowHash(row Row) (bool, string, error) {
	// The ledger has only ever written versions 1 through the current one, so
	// any other claim is a forged label, not an unknown writer.
	if row.HashVersion < auditHashVersion1 || row.HashVersion > auditHashVersionCurrent {
		return false, fmt.Sprintf("claims hash version %d, which the ledger never wrote", row.HashVersion), nil
	}
	// The column stores microseconds, so every stored timestamp is a whole
	// number of them. The candidate search below adds a remainder to the
	// stored value, so a row that already carries one would let a forger move
	// a version 3 row's time and relabel the row as legacy: the search would
	// rediscover the original instant and accept the edit. A stored remainder
	// is therefore a forged row, not history.
	if row.HashVersion < auditHashVersion3 && row.EventTime.Nanosecond()%legacyNanosecondCandidates != 0 {
		return false, fmt.Sprintf(
			"claims hash version %d with a sub-microsecond timestamp the ledger cannot store",
			row.HashVersion), nil
	}
	piiRef := uuid.Nil
	if row.PIIRef != nil {
		piiRef = *row.PIIRef
	}
	// The writer hashed the JSON it marshalled from these same types, and the
	// hash canonicalizes by parsed value, so re-marshalling the decoded row
	// reproduces the covered bytes key for key.
	contextJSON, err := json.Marshal(row.Context)
	if err != nil {
		slog.Error("audit.export.row_context_encode_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return false, "", fmt.Errorf("verify row %s context: %w", row.EventID, err)
	}
	deltaJSON, err := json.Marshal(row.Delta)
	if err != nil {
		slog.Error("audit.export.row_delta_encode_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return false, "", fmt.Errorf("verify row %s delta: %w", row.EventID, err)
	}
	input := rowHashInput{
		Event:   exportEvent(row),
		EventID: row.EventID, Shard: row.Shard, Seq: row.Seq,
		PIIRef: piiRef, ContextJSON: contextJSON, DeltaJSON: deltaJSON,
		LastHash: row.PrevHash, Version: row.HashVersion,
	}
	candidates := 1
	if row.HashVersion < auditHashVersion3 {
		candidates = legacyNanosecondCandidates
	}
	base := row.EventTime
	for remainder := range candidates {
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

// exportEvent rebuilds the hash-relevant event from a stored row. The hash
// payload reads its org through Context.OrgID, so that field carries the
// row's org_id column: the stored context JSON is hashed separately from
// row.Context itself, and this keeps an edit to either the column or the
// context visible.
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
