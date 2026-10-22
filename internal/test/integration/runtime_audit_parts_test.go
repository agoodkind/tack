package integration_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/twmb/franz-go/pkg/kgo"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// largeDescriptionBytes sets the target size of each synthetic description.
	largeDescriptionBytes = 60 * 1024
	// foundationDBValueLimit sets the single-value size that the large delta
	// test must exceed.
	foundationDBValueLimit = 100_000
	// eventRowsQuery counts ledger rows for one event ID.
	eventRowsQuery = `SELECT count(*) FROM audit.events WHERE event_id = $1`
	// assemblyRowsQuery counts transient assemblies for one event ID.
	assemblyRowsQuery = `SELECT count(*) FROM audit.event_assemblies WHERE event_id = $1`
	// assemblyPartRowsQuery counts transient assembly parts for one event ID.
	assemblyPartRowsQuery = `SELECT count(*) FROM audit.event_assembly_parts WHERE event_id = $1`
	// completedRowsQuery counts completed transport records for one event ID.
	completedRowsQuery = `SELECT count(*) FROM audit.completed_transports WHERE event_id = $1`
	// outboxRowsQuery counts YugabyteDB outbox rows for one event ID.
	outboxRowsQuery = `SELECT count(*) FROM public.ops_outbox WHERE event_id = $1`
)

// TestRuntimeAuditLargeDeltaDelivery creates and updates an issue through
// the audited MCP handler with descriptions that produce a delta larger
// than one FoundationDB value. It requires the relay and consumer to preserve
// both descriptions, record one completed transport, remove the assembly,
// and produce an export that passes chain verification.
func TestRuntimeAuditLargeDeltaDelivery(t *testing.T) {
	harness := integration.NewAuditedMCPHarness(t)
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), nodeAuditTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	first := strings.Repeat("first synthetic description line\n", largeDescriptionBytes/len("first synthetic description line\n"))
	second := strings.Repeat("second synthetic description line\n", largeDescriptionBytes/len("second synthetic description line\n"))
	createArguments := auditedIssueArguments(harness)
	createArguments.ProjectReference = harness.Project
	createArguments.Name = "large delta issue"
	createArguments.Properties = datagen.NodeProperties{"description": json.RawMessage(strconv.Quote(first))}
	issueID := uuid.MustParse(harness.Call(t, "tack_create_issue", createArguments).RawID())
	updateArguments := auditedIssueArguments(harness)
	updateArguments.NodeID = issueID.String()
	updateArguments.Properties = datagen.NodeProperties{"description": json.RawMessage(strconv.Quote(second))}
	harness.Call(t, "tack_update_issue", updateArguments)
	view, err := stores.Views.Get(t.Context(), issueID)
	if err != nil || view == nil {
		t.Fatalf("issue %s after the update = %v, %v", issueID, view, err)
	}

	ledger := startNodeAuditLedger(t)
	row := ledger.entityRows(t, view.OrgID, issueID, audit.VerbNodeUpdate, 1)[0]
	if row.Delta == nil || len(row.Delta.Before)+len(row.Delta.After) <= foundationDBValueLimit {
		t.Fatalf("update delta of %s is not larger than one FoundationDB value", issueID)
	}
	before := decodeAuditedIssueFields(t, row.Delta.Before)
	after := decodeAuditedIssueFields(t, row.Delta.After)
	if string(before.Props["description"]) != strconv.Quote(first) || string(after.Props["description"]) != strconv.Quote(second) {
		t.Fatalf("update delta of %s does not contain the complete old and new descriptions", issueID)
	}
	if countRows(t, ledger.admin, completedRowsQuery, row.EventID) != 1 || countRows(t, ledger.admin, assemblyRowsQuery, row.EventID) != 0 {
		t.Fatalf("event %s did not complete through part delivery", row.EventID)
	}
	ledger.requireChainVerifies(t, view.OrgID)
}

// TestRuntimeAuditPartCrashRecovery interrupts delivery after two broker
// acknowledgments and restarts the consumer after it stores two parts.
// It interrupts a second delivery before outbox deletion, then replays every
// record. It requires one complete ledger event, one completed transport,
// transient state cleanup, an empty dead-letter table, and a verified export.
func TestRuntimeAuditPartCrashRecovery(t *testing.T) {
	pipeline := newPartsPipeline(t)
	oldest := clock.Now().UTC().Add(-time.Minute)
	event := largeChangeEvent(uuid.Must(uuid.NewV7()))
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil || len(encoded.Parts) < 2 {
		t.Fatalf("encode event %s into parts: %d parts, %v", event.EventID, len(encoded.Parts), err)
	}
	records := len(encoded.Parts) + 1
	if err := audit.NewPoolOutbox(pipeline.admin).WriteOutbox(t.Context(), event); err != nil {
		t.Fatalf("write event %s to the outbox: %v", event.EventID, err)
	}

	firstConsumer := pipeline.startConsumer(t)
	pipeline.deliverUntilStopped(t, event.EventID, 2)
	waitFor(t, "storage of two parts", func() bool { return pipeline.count(t, assemblyPartRowsQuery, event.EventID) == 2 })
	_ = firstConsumer.Close()
	if pipeline.count(t, eventRowsQuery, event.EventID) != 0 || pipeline.count(t, outboxRowsQuery, event.EventID) != 1 {
		t.Fatalf("event %s after interrupted delivery: want no ledger row and one outbox row", event.EventID)
	}

	pipeline.deliverUntilStopped(t, event.EventID, records)
	if pipeline.count(t, outboxRowsQuery, event.EventID) != 1 {
		t.Fatalf("event %s outbox row was deleted before the relay finished", event.EventID)
	}
	pipeline.startConsumer(t)
	row := pipeline.stack().waitForRow(t, event.EventID)
	pipeline.startRelay(t, nil)
	waitFor(t, "outbox delivery", func() bool { return pipeline.count(t, outboxRowsQuery, event.EventID) == 0 })
	pipeline.waitForConsumed(t)

	for query, want := range map[string]int64{
		eventRowsQuery: 1, assemblyRowsQuery: 0, assemblyPartRowsQuery: 0, completedRowsQuery: 1,
	} {
		if got := pipeline.count(t, query, event.EventID); got != want {
			t.Fatalf("%s for %s = %d, want %d", query, event.EventID, got, want)
		}
	}
	requireSameDelta(t, event, row)
	pipeline.stack().waitForDeadLetters(t, 0)
	if err := pipeline.stack().exportAndVerify(t, event.Context.OrgID, oldest, clock.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("export of event %s failed verification: %v", event.EventID, err)
	}
}

// TestRuntimeAuditPartConflict stores an altered part with a matching part
// digest before delivering the genuine messages. It requires a dead letter
// for the conflicting genuine part, an invalid assembly, and rejection of
// the event from the ledger.
func TestRuntimeAuditPartConflict(t *testing.T) {
	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	event := largeChangeEvent(uuid.Must(uuid.NewV7()))
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		t.Fatalf("encode event %s: %v", event.EventID, err)
	}
	messages, err := encoded.Messages()
	if err != nil || len(messages) < 3 {
		t.Fatalf("encode event %s messages: %d, %v", event.EventID, len(messages), err)
	}
	producer, err := kgo.NewClient(kgo.SeedBrokers(pipeline.brokers), kgo.DefaultProduceTopic(pipeline.topic))
	if err != nil {
		t.Fatalf("open the test producer: %v", err)
	}
	t.Cleanup(producer.Close)
	produce := func(value []byte) {
		t.Helper()
		record := &kgo.Record{Key: messageKey(t, value), Value: value}
		if err := producer.ProduceSync(t.Context(), record).FirstErr(); err != nil {
			t.Fatalf("produce a message of event %s: %v", event.EventID, err)
		}
	}

	produce(alteredPart(t, messages[1]))
	waitFor(t, "storage of the altered part", func() bool { return pipeline.count(t, assemblyPartRowsQuery, event.EventID) == 1 })
	for _, message := range messages {
		produce(message)
	}
	errorTexts := pipeline.stack().waitForDeadLetters(t, 1)
	if !strings.Contains(errorTexts[0], event.EventID.String()) || !strings.Contains(errorTexts[0], "conflicts with the stored part") {
		t.Fatalf("dead letter error = %q, want the conflicting part of event %s", errorTexts[0], event.EventID)
	}
	waitFor(t, "rejection of the assembly", func() bool {
		return pipeline.count(t, `SELECT count(*) FROM audit.event_assemblies WHERE event_id = $1 AND invalid_reason IS NOT NULL`, event.EventID) == 1
	})
	if _, err := pipeline.reader.GetByID(t.Context(), event.EventID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ledger read of event %s with an altered part = %v, want no row", event.EventID, err)
	}
}
