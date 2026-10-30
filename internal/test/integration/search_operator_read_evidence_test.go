package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

func requireSearchReadLedgerIdentity(t *testing.T, row audit.Row, event audit.Event) {
	t.Helper()
	if row.ActorID != event.Actor.ID || row.Context.AuditFormatVersion != event.Context.AuditFormatVersion ||
		row.Context.OperationClass != event.Context.OperationClass || row.Context.Phase != event.Context.Phase ||
		row.Context.OperationID != event.Context.OperationID || row.Context.Target == nil ||
		*row.Context.Target != *event.Context.Target || !row.Context.StartedAt.Equal(event.Context.StartedAt) ||
		!row.Context.CompletedAt.Equal(event.Context.CompletedAt) {
		t.Fatalf("permanent actor, target, operation, or times differ from final event %s: %+v", event.EventID, row)
	}
	var actual, expected struct {
		commandExtra
		DeployCommit string `json:"deploy_commit"`
	}
	if err := json.Unmarshal(row.Extra, &actual); err != nil {
		t.Fatalf("decode permanent operator evidence: %v", err)
	}
	if err := json.Unmarshal(event.Extra, &expected); err != nil {
		t.Fatalf("decode finalized operator evidence: %v", err)
	}
	if actual.OpID != expected.OpID || actual.DeployCommit != expected.DeployCommit ||
		actual.SessionID != expected.SessionID || actual.OnBehalfOf == nil || expected.OnBehalfOf == nil ||
		actual.OnBehalfOf.OperatorID != expected.OnBehalfOf.OperatorID {
		t.Fatalf("permanent operator evidence differs from final event %s: %+v", event.EventID, actual)
	}
}

func requireSearchReadEvent(t *testing.T, pool *pgxpool.Pool, operatorID uuid.UUID, verb audit.Verb, reference string) audit.Event {
	t.Helper()
	actorID := cli.ServiceActorID(searchReadService(operatorID))
	events := readOutboxEvents(t, pool, verb, actorID)
	if len(events) != 1 {
		t.Fatalf("%s recorded %d events instead of one final read: %+v", verb, len(events), events)
	}
	event := events[0]
	if err := audit.ValidateEvent(event); err != nil {
		t.Fatalf("%s recorded an invalid final event: %v", verb, err)
	}
	if event.Actor.Type != audit.ActorService || event.Actor.ID != actorID || event.Actor.SessionID != searchReadSession ||
		event.Context.AuditFormatVersion != audit.AuditFormatComplete || event.Context.OperationClass != audit.OperationRead ||
		event.Context.Phase != audit.PhaseRead || event.Context.OperationID == uuid.Nil ||
		event.Context.StartedAt.IsZero() || event.Context.CompletedAt.Before(event.Context.StartedAt) ||
		!event.OccurredAt.Equal(event.Context.StartedAt) || event.Context.Target == nil ||
		event.Context.Target.Reference != reference {
		t.Fatalf("%s did not record the actual actor, target, operation, and times: %+v", verb, event)
	}
	var extra struct {
		commandExtra
		DeployCommit string          `json:"deploy_commit"`
		Input        json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode %s operator evidence: %v", verb, err)
	}
	if extra.OpID != event.Context.OperationID || extra.DeployCommit != searchReadDeployCommit ||
		extra.SessionID != searchReadSession || extra.OnBehalfOf == nil || extra.OnBehalfOf.OperatorID != operatorID ||
		string(extra.Input) != "{}" {
		t.Fatalf("%s operator evidence = %+v, want the accountable operator, deployment, and safe input", verb, extra)
	}
	return event
}

func requireSearchReadPayload(t *testing.T, event audit.Event, payload []byte, targets []audit.Target) {
	t.Helper()
	digest, parts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("digest expected read targets: %v", err)
	}
	evidence := event.Context.Read
	if event.Outcome != audit.OutcomeCompleted || evidence == nil || evidence.Count != uint64(len(targets)) ||
		evidence.PayloadSHA256 != payloadDigest(payload) || evidence.PayloadFormat != "application/json" ||
		evidence.ManifestSHA256 != digest || evidence.ManifestParts != parts {
		t.Fatalf("%s read evidence = %+v, want the exact payload and ordered targets %+v", event.Verb, evidence, targets)
	}
}
