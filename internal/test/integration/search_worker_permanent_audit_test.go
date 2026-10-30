package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const searchWorkerServiceActorKind int16 = 2

// requirePermanentSearchWorkerEvents delivers actual worker events through the
// production relay and consumer, then checks permanent ledger rows and their export.
func requirePermanentSearchWorkerEvents(t *testing.T, stores *fdbadapter.Stores, events []audit.Event) []audit.Row {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("the search worker did not record structural events for permanent-ledger verification")
	}
	stack := startRuntimeAuditStack(t)
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic, YugabyteDSN: testenv.Ledger(t),
		FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: integration.StoreTransactionTimeout,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("open the search worker audit relay: %v", err)
	}
	opened.Relay.Start(t.Context())
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() {
		if err := opened.Relay.Close(); err != nil {
			t.Errorf("close the search worker audit relay: %v", err)
		}
	})
	rows := make([]audit.Row, 0, len(events))
	oldest := events[0].OccurredAt
	for _, event := range events {
		row := stack.waitForRow(t, event.EventID)
		requirePermanentWorkerRow(t, row, event)
		rows = append(rows, row)
		if event.OccurredAt.Before(oldest) {
			oldest = event.OccurredAt
		}
	}
	if err := stack.exportAndVerify(t, audit.SystemOrgID(), oldest.Add(-time.Second), clock.Now().Add(time.Second)); err != nil {
		t.Fatalf("verify the permanent search worker audit export: %v", err)
	}
	if remaining := searchWorkerEvents(t, stores, nil); len(remaining) != 0 {
		t.Fatalf("the delivered search worker outbox retains %d events", len(remaining))
	}
	return rows
}

func requirePermanentWorkerRow(t *testing.T, row audit.Row, original audit.Event) {
	t.Helper()
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	if row.ActorID != workerID || row.ActorKind != searchWorkerServiceActorKind || row.OrgID != audit.SystemOrgID() ||
		row.Action != original.Verb || row.Outcome != original.Outcome || row.Seq <= 0 || len(row.RowHash) != sha256.Size {
		t.Fatalf("permanent search worker row %s has actor %s/%d, action %s, outcome %s, sequence %d", row.EventID, row.ActorID, row.ActorKind, row.Action, row.Outcome, row.Seq)
	}
	actual, expected := row.Context, original.Context
	if actual.AuditFormatVersion != audit.AuditFormatComplete || actual.OperationClass != audit.OperationExternal || actual.Source != audit.SourceSystem ||
		actual.OperationID != expected.OperationID || actual.ParentOperationID != expected.ParentOperationID || actual.Phase != expected.Phase ||
		actual.Target == nil || expected.Target == nil || *actual.Target != *expected.Target ||
		!actual.StartedAt.Equal(expected.StartedAt) || !actual.CompletedAt.Equal(expected.CompletedAt) ||
		!row.EventTime.Equal(original.OccurredAt.Truncate(time.Microsecond)) {
		t.Fatalf("permanent search worker event %s changed its operation, target, phase, or original timing", row.EventID)
	}
	requireWorkerLedgerExtra(t, row, original)
}

func requireWorkerLedgerExtra(t *testing.T, row audit.Row, original audit.Event) {
	t.Helper()
	if len(original.Extra) == 0 && len(row.Extra) == 0 {
		return
	}
	var expected, actual map[string]json.RawMessage
	if err := json.Unmarshal(original.Extra, &expected); err != nil {
		t.Fatalf("decode original worker evidence %s: %v", original.EventID, err)
	}
	if err := json.Unmarshal(row.Extra, &actual); err != nil {
		t.Fatalf("decode permanent worker evidence %s: %v", row.EventID, err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("permanent worker event %s has %d evidence fields, want %d", row.EventID, len(actual), len(expected))
	}
	for name, value := range expected {
		if !bytes.Equal(value, actual[name]) {
			t.Fatalf("permanent worker event %s changed evidence field %s", row.EventID, name)
		}
	}
}

// requirePermanentMirrorAudit verifies completed creation and alias evidence
// after cancellation or after the paused rebuild resumes copying.
func requirePermanentMirrorAudit(t *testing.T, env *migrationEnv, target string) {
	t.Helper()
	rows := requirePermanentSearchWorkerEvents(t, env.Stores, searchWorkerEvents(t, env.Stores, nil))
	created, switched := false, false
	for _, row := range rows {
		if row.Context.Phase != audit.PhaseResult || row.Context.ParentOperationID == uuid.Nil || row.Outcome != audit.OutcomeCompleted {
			continue
		}
		var evidence struct {
			Action      string `json:"action"`
			SourceIndex string `json:"source_index"`
			TargetIndex string `json:"target_index"`
			AliasIndex  string `json:"alias_index"`
		}
		if err := json.Unmarshal(row.Extra, &evidence); err != nil {
			t.Fatalf("decode completed mirror evidence %s: %v", row.EventID, err)
		}
		if evidence.SourceIndex != env.Index || evidence.TargetIndex != target {
			t.Fatalf("permanent mirror event %s records source %s and target %s", row.EventID, evidence.SourceIndex, evidence.TargetIndex)
		}
		if evidence.Action == "create_replacement" && row.Context.Target.Reference == "opensearch/index/"+target {
			created = true
		}
		if evidence.Action == "switch_alias" && evidence.AliasIndex == target && row.Context.Target.Reference == "opensearch/public-alias" {
			switched = true
		}
	}
	if !created || !switched {
		t.Fatalf("permanent mirror audit has completed creation %t and alias switch %t, want both", created, switched)
	}
}
