package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// requireStoredAuditReads polls the ledger until the operator has one audit.read row per run. It fails if the operator has more rows or the deadline passes.
func requireStoredAuditReads(t *testing.T, ledger *nodeAuditLedger, operatorID uuid.UUID, runs []manifestReadRun) {
	t.Helper()
	now := clock.Now()
	filter := audit.QueryFilter{
		OrgID: audit.SystemOrgID(), Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: string(audit.VerbAuditRead), ActorID: operatorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: len(runs) + 1,
	}
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows, err := ledger.reader.Query(t.Context(), filter)
		if err != nil {
			t.Fatalf("query the audit.read rows of operator %s: %v", operatorID, err)
		}
		if len(rows) > len(runs) {
			t.Fatalf("ledger has %d audit.read rows for operator %s, want %d", len(rows), operatorID, len(runs))
		}
		if len(rows) == len(runs) {
			requireRowsMatchRuns(t, rows, runs)
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger has %d audit.read rows for operator %s after %s, want %d", len(rows), operatorID, nodeAuditDeadline, len(runs))
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

// requireRowsMatchRuns matches each row to one run by safe input and evidence. It fails if a row does not match.
func requireRowsMatchRuns(t *testing.T, rows []audit.Row, runs []manifestReadRun) {
	t.Helper()
	remaining := slices.Clone(runs)
	for _, row := range rows {
		var extra struct {
			Input struct {
				Org     string `json:"org"`
				EventID string `json:"event_id"`
			} `json:"input"`
		}
		if err := json.Unmarshal(row.Extra, &extra); err != nil {
			t.Fatalf("decode the extra of row %s: %v\n%s", row.EventID, err, row.Extra)
		}
		position := slices.IndexFunc(remaining, func(run manifestReadRun) bool {
			return run.orgID.String() == extra.Input.Org && run.eventID.String() == extra.Input.EventID && manifestRowMatches(row, run)
		})
		if position < 0 {
			t.Fatalf("row %s does not match a remaining run: %+v context %+v error %+v, runs %+v", row.EventID, extra.Input, row.Context, row.Error, remaining)
		}
		remaining = slices.Delete(remaining, position, position+1)
	}
}

// manifestRowMatches reports whether a row has the outcome, class, target, and read evidence of a run.
func manifestRowMatches(row audit.Row, run manifestReadRun) bool {
	eventContext := row.Context
	if row.Outcome != run.outcome || eventContext.OperationClass != audit.OperationRead ||
		eventContext.Target == nil || eventContext.Target.URI != "tack:audit-event/"+run.eventID.String() {
		return false
	}
	if run.outcome == audit.OutcomeFailed {
		return eventContext.Read == nil && row.Error != nil && row.Error.Code == "read_failed"
	}
	return eventContext.Read != nil && eventContext.Read.PayloadSHA256 == run.payloadSHA256 && eventContext.Read.Count == run.targetCount
}

// storeManifestRead writes one completed read event and its targets to the outbox. It returns the event ID and stored parts after the reader finds at least two declared parts.
func storeManifestRead(t *testing.T, ledger *nodeAuditLedger, orgID uuid.UUID) (uuid.UUID, []audit.ManifestPart) {
	t.Helper()
	first := readNodeTarget()
	targets := syntheticReadTargets(first)
	manifestDigest, manifestParts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("compute the read manifest digest: %v", err)
	}
	payloadDigest := sha256.Sum256([]byte(first.ID.String()))
	read := completeEvent(orgID, userActor(), first, audit.OperationRead, audit.PhaseRead, audit.OutcomeCompleted)
	read.Context.Read = &audit.ReadEvidence{
		Count: readTargetCount, PayloadSHA256: hex.EncodeToString(payloadDigest[:]), PayloadFormat: "application/json",
		ManifestSHA256: manifestDigest, ManifestParts: manifestParts,
	}
	encoded, err := audit.EncodeEvent(read, targets)
	if err != nil {
		t.Fatalf("encode event %s: %v", read.EventID, err)
	}
	transaction, err := ledger.admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the outbox write of event %s: %v", read.EventID, err)
	}
	defer func() { _ = transaction.Rollback(context.WithoutCancel(t.Context())) }()
	if err := audit.WriteEncodedOutboxTx(t.Context(), transaction, encoded); err != nil {
		t.Fatalf("write event %s to the outbox: %v", read.EventID, err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit the outbox write of event %s: %v", read.EventID, err)
	}
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		parts, err := ledger.reader.ManifestParts(t.Context(), orgID, read.EventID)
		if err != nil {
			t.Fatalf("read the manifest parts of event %s: %v", read.EventID, err)
		}
		if uint32(len(parts)) == manifestParts && manifestParts >= 2 {
			return read.EventID, parts
		}
		if clock.Now().After(deadline) {
			t.Fatalf("event %s stored %d manifest parts after %s, want %d", read.EventID, len(parts), nodeAuditDeadline, manifestParts)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
