package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

// recordLedgerVerbEvents records one system-organization operator event per
// recorded ledger verb through the production Kafka recorder.
func recordLedgerVerbEvents(t *testing.T, cfg *config.Config) {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(cfg.AuditKafkaBrokers), Topic: cfg.AuditKafkaTopic,
		ClientID: "tack-ledger-verbs-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the audit producer: %v", err)
	}
	for _, verb := range recordedLedgerVerbs {
		event := audit.Event{
			Verb: string(verb), EventID: uuid.Must(uuid.NewV7()),
			Actor: audit.Actor{
				Type: audit.ActorOperator, ID: uuid.Must(uuid.NewV7()), Email: "", Name: "Ledger Verbs Test",
				SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
			},
			Entity: audit.Entity{Type: "system", NodeType: "", ID: audit.SystemOrgID(), Identifier: "", Name: ""},
			Context: audit.EventContext{
				OrgID: audit.SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
				RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
			},
			Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
			OccurredAt: clock.Now().UTC(), Extra: nil,
		}
		if err := recorder.Record(t.Context(), event); err != nil {
			t.Fatalf("record a %s event: %v", verb, err)
		}
	}
	if err := recorder.CloseContext(t.Context()); err != nil {
		t.Fatalf("flush the audit producer: %v", err)
	}
}

// waitForLedgerVerbRows reads the ledger through the reader login until each
// recorded verb has a row on the system organization.
func waitForLedgerVerbRows(t *testing.T, cfg *config.Config) {
	t.Helper()
	reader, err := audit.NewReader(t.Context(), cfg.AuditReaderDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	defer reader.Close()
	verbs := make([]string, 0, len(recordedLedgerVerbs))
	for _, verb := range recordedLedgerVerbs {
		verbs = append(verbs, string(verb))
	}
	deadline := clock.Now().Add(ledgerVerbsDeadline)
	for {
		presence, err := reader.VerbPresence(t.Context(), audit.SystemOrgID(), verbs)
		if err != nil {
			t.Fatalf("read the ledger verb presence: %v", err)
		}
		projected := 0
		for _, verb := range presence {
			if verb.Rows > 0 {
				projected++
			}
		}
		if projected == len(verbs) {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger verb presence after %s = %+v, want a row for each of %v", ledgerVerbsDeadline, presence, verbs)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
