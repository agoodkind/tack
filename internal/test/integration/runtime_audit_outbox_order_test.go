package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// transactionVersionLength is the number of characters in the transaction
// version prefix of a commit version.
const transactionVersionLength = 20

// TestRuntimeAuditOutboxSharedTransaction writes a single-record encoded
// event, a multipart encoded event, and plain event JSON in one FoundationDB
// transaction with one writer. It checks every outbox entry and uses the real
// relay and Kafka to check that all three commit versions share a transaction
// version and increase in write order.
func TestRuntimeAuditOutboxSharedTransaction(t *testing.T) {
	database, err := fdbadapter.Open(testenv.FoundationDB(t), nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open FoundationDB: %v", err)
	}
	prefixID := uuid.New()
	fdbadapter.SetTestPrefix(append([]byte("tack-test:"), prefixID[:]...))
	t.Cleanup(func() { fdbadapter.SetTestPrefix(nil) })
	orgID := uuid.Must(uuid.NewV7())
	single, err := audit.EncodeEvent(smallChangeEvent(orgID), nil)
	if err != nil || single.Multipart() {
		t.Fatalf("encode the single record event: multipart %t, %v", single.Multipart(), err)
	}
	large, err := audit.EncodeEvent(largeChangeEvent(orgID), nil)
	if err != nil || !large.Multipart() {
		t.Fatalf("encode the multipart event: multipart %t, %v", large.Multipart(), err)
	}
	plain := smallChangeEvent(orgID)
	plainJSON, err := audit.MarshalEvent(plain)
	if err != nil {
		t.Fatalf("encode the plain event: %v", err)
	}
	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		outbox := fdbadapter.NewOutboxWriter(tr)
		if err := outbox.WriteEncodedEvent(t.Context(), single); err != nil {
			return nil, err
		}
		if err := outbox.WriteEncodedEvent(t.Context(), large); err != nil {
			return nil, err
		}
		return nil, outbox.WriteEventJSON(t.Context(), plainJSON)
	})
	if err != nil {
		t.Fatalf("write three events in one transaction: %v", err)
	}
	store := fdbadapter.NewOpsOutboxStore(database)
	entries, err := store.ReadOutboxFrom(t.Context(), nil, 100)
	if err != nil || len(entries) != len(large.Parts)+3 {
		t.Fatalf("outbox entries = %d, %v, want %d", len(entries), err, len(large.Parts)+3)
	}

	topic := "audit.outbox-order-test-" + uuid.NewString()[:8]
	brokers := testenv.Kafka(t)
	ledgerDSN := testenv.Ledger(t)
	topicOwner, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-outbox-order-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: ledgerDSN,
	})
	if err != nil {
		t.Fatalf("create the audit topic %s: %v", topic, err)
	}
	topicOwner.Start(t.Context())
	t.Cleanup(func() { _ = topicOwner.Close() })
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{brokers}, Topic: topic, YugabyteDSN: ledgerDSN,
		FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: nodeAuditTransactionTimeout,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("open the relay: %v", err)
	}
	opened.Relay.Start(t.Context())
	t.Cleanup(func() {
		_ = opened.Relay.Close()
		opened.Pool.Close()
	})

	versions := relayedCommitVersions(t, brokers, topic, map[uuid.UUID]bool{single.EventID: true, large.EventID: true, plain.EventID: true})
	ordered := []string{versions[single.EventID], versions[large.EventID], versions[plain.EventID]}
	for position, version := range ordered {
		if len(version) != commitVersionLength || version[:transactionVersionLength] != ordered[0][:transactionVersionLength] {
			t.Fatalf("commit versions %v do not share one transaction version", ordered)
		}
		if position > 0 && ordered[position-1] >= version {
			t.Fatalf("commit versions %v are not distinct and in write order", ordered)
		}
	}
}

// smallChangeEvent returns a small synthetic format 2 change event.
func smallChangeEvent(orgID uuid.UUID) audit.Event {
	target := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "",
		RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	event := completeEvent(orgID, userActor(), target, audit.OperationDatabaseChange, audit.PhaseChange, audit.OutcomeCompleted)
	event.Delta = &audit.Delta{Before: nil, After: json.RawMessage(`{"name":"synthetic"}`), Changed: []string{"name"}}
	return event
}

// relayedCommitVersions reads the topic until it finds each wanted event's
// commit version in plain event JSON or a multipart manifest. It fails the
// test if the deadline passes.
func relayedCommitVersions(t *testing.T, brokers, topic string, wanted map[uuid.UUID]bool) map[uuid.UUID]string {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("open the test consumer: %v", err)
	}
	t.Cleanup(client.Close)
	versions := make(map[uuid.UUID]string, len(wanted))
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for len(versions) < len(wanted) {
		if clock.Now().After(deadline) {
			t.Fatalf("relay delivered commit versions %v within %s, want %d events", versions, runtimeAuditDeadline, len(wanted))
		}
		pollContext, cancel := context.WithTimeout(t.Context(), time.Second)
		fetches := client.PollFetches(pollContext)
		cancel()
		fetches.EachRecord(func(record *kgo.Record) {
			var message struct {
				Kind          string             `json:"audit_message"`
				EventID       uuid.UUID          `json:"event_id"`
				CommitVersion string             `json:"commit_version"`
				Context       audit.EventContext `json:"context"`
			}
			if json.Unmarshal(record.Value, &message) != nil || !wanted[message.EventID] {
				return
			}
			switch message.Kind {
			case "":
				versions[message.EventID] = message.Context.CommitVersion
			case "event_manifest":
				versions[message.EventID] = message.CommitVersion
			}
		})
	}
	return versions
}
