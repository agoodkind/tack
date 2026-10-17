package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/ops"
)

// outboxEvent returns the FoundationDB operator outbox payload for the single
// event that matches verb and entityID.
func outboxEvent(t *testing.T, stores *fdbadapter.Stores, entityID uuid.UUID, verb audit.Verb) json.RawMessage {
	t.Helper()
	var found []json.RawMessage
	var mark []byte
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, 500)
		if err != nil {
			t.Fatalf("read the operator outbox: %v", err)
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			var event audit.Event
			if err := json.Unmarshal(entry.Event, &event); err != nil {
				t.Fatalf("decode an outbox entry: %v", err)
			}
			if event.Entity.ID == entityID && event.Verb == string(verb) {
				found = append(found, entry.Event)
			}
		}
		mark = entries[len(entries)-1].Mark
	}
	if len(found) != 1 {
		t.Fatalf("outbox entries of %s for %s = %d, want 1", verb, entityID, len(found))
	}
	return found[0]
}

// redeliver produces payload to the test topic again through the production
// Kafka recorder. It reproduces a relay delivery that succeeds before the
// relay fails to clear the outbox entry. It returns after ops audit
// consumer-offsets reports zero lag.
func (l *nodeAuditLedger) redeliver(t *testing.T, payload json.RawMessage) {
	t.Helper()
	var event audit.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode the redelivered event: %v", err)
	}
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(l.cfg.AuditKafkaBrokers), Topic: l.cfg.AuditKafkaTopic,
		ClientID: "tack-node-integrity-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the audit producer: %v", err)
	}
	if err := recorder.Record(t.Context(), event); err != nil {
		t.Fatalf("redeliver event %s: %v", event.EventID, err)
	}
	if err := recorder.CloseContext(t.Context()); err != nil {
		t.Fatalf("flush the audit producer: %v", err)
	}
	l.waitForNoLag(t)
}

// waitForNoLag runs ops audit consumer-offsets through the audited command
// tree until the consumer group reports zero lag on the test topic.
func (l *nodeAuditLedger) waitForNoLag(t *testing.T) {
	t.Helper()
	output := &bytes.Buffer{}
	factory := cli.System(l.cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(l.admin))
	t.Cleanup(factory.CloseAuditOutbox)
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		output.Reset()
		root := nodeAuditCommandRoot(factory)
		root.SetContext(t.Context())
		root.SetArgs([]string{
			"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca31",
			"--operator-email", "operator@example.com", "--operator-name", "Node Audit Test",
			"ops", "audit", "consumer-offsets",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("ops audit consumer-offsets: %v\n%s", err, output.String())
		}
		var report struct {
			Result struct {
				TotalLag int64 `json:"total_lag"`
			} `json:"result"`
		}
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatalf("decode the consumer-offsets output %q: %v", output.String(), err)
		}
		if report.Result.TotalLag == 0 {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("total lag = %d after %s, want 0", report.Result.TotalLag, nodeAuditDeadline)
		}
		time.Sleep(time.Second)
	}
}

// nodeAuditCommandRoot renders the registered ops commands under one root
// with the server root command's global flags and identity source.
func nodeAuditCommandRoot(factory *cli.Factory) *cobra.Command {
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	return root
}

// requireChainVerifies exports ledger rows for orgID through audit.Export and
// verifies the bundle with audit.VerifyBundle. The audit export and audit
// verify commands use these operations.
func (l *nodeAuditLedger) requireChainVerifies(t *testing.T, orgID uuid.UUID) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the export key: %v", err)
	}
	directory := t.TempDir()
	manifest, err := audit.Export(t.Context(), l.reader, privateKey, "ed25519:node-integrity-test", audit.QueryFilter{
		OrgID: orgID, Oldest: time.Unix(0, 0).UTC(), Latest: clock.Now().Add(time.Hour),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 0,
	}, directory)
	if err != nil {
		t.Fatalf("export the ledger of %s: %v", orgID, err)
	}
	report, err := audit.VerifyBundle(directory, publicKey)
	if err != nil {
		t.Fatalf("verify the ledger bundle of %s: %v", orgID, err)
	}
	if manifest.RowCount == 0 || report.HashMatches != manifest.RowCount || len(report.ChainBreaks) != 0 ||
		report.ChainGapCount != 0 || report.Err() != nil {
		t.Fatalf("chain verify of %d rows: hash matches %d, breaks %v, gaps %d, verdict %v",
			manifest.RowCount, report.HashMatches, report.ChainBreaks, report.ChainGapCount, report.Err())
	}
}
