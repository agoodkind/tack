package integration_test

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"goodkind.io/tack/internal/audit"
)

// legacyRevisions lists the Tack revisions that wrote the legacy fixtures.
// Each revision ran the program in testdata/legacy_audit/generator with
// `go run ./cmd/legacyfixture <revision>` and wrote
// testdata/legacy_audit/<revision>.json.
var legacyRevisions = []string{"460d25a9", "bb9792cd"}

// legacyFixture is one record that a legacy writer produced: a Kafka record,
// a public.ops_outbox row, a FoundationDB operator outbox entry, or the payload
// of a record that the legacy consumer dead-lettered.
type legacyFixture struct {
	Kind    string    `json:"kind"`
	EventID uuid.UUID `json:"event_id"`
	OrgID   uuid.UUID `json:"org_id"`
	Verb    string    `json:"verb"`
	// PartitionBytes is the Kafka record key in hexadecimal.
	PartitionBytes string `json:"partition_bytes"`
	// Value is the record value, which every legacy writer encodes as JSON.
	Value      string    `json:"value"`
	occurredAt time.Time `exhaustruct:"optional"`
}

// legacyFixtures reads the fixtures of revision with kind and creates the
// audit.events partition for the week of each fixture event.
func legacyFixtures(t *testing.T, revision, kind string) []legacyFixture {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "legacy_audit", revision+".json"))
	if err != nil {
		t.Fatalf("Reading the %s legacy fixtures failed: %v", revision, err)
	}
	var all []legacyFixture
	if err := json.Unmarshal(contents, &all); err != nil {
		t.Fatalf("Decoding the %s legacy fixtures failed: %v", revision, err)
	}
	admin := ledgerAdminPool(t)
	var selected []legacyFixture
	for _, fixture := range all {
		if fixture.Kind != kind {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal([]byte(fixture.Value), &event); err != nil {
			t.Fatalf("Decoding the %s fixture event %s failed: %v", revision, fixture.EventID, err)
		}
		fixture.occurredAt = event.OccurredAt
		if _, err := admin.Exec(t.Context(), readTestSQL(t, "legacy_audit/event_partition.sql"), event.OccurredAt); err != nil {
			t.Fatalf("Creating the audit.events partition for %s failed: %v", event.OccurredAt, err)
		}
		selected = append(selected, fixture)
	}
	if len(selected) == 0 {
		t.Fatalf("The %s legacy fixtures do not contain a %s record", revision, kind)
	}
	return selected
}

// produceLegacyRecords produces each fixture key and value unchanged to the
// pipeline topic.
func produceLegacyRecords(t *testing.T, pipeline partsPipeline, fixtures []legacyFixture) {
	t.Helper()
	producer, err := kgo.NewClient(kgo.SeedBrokers(pipeline.brokers), kgo.DefaultProduceTopic(pipeline.topic))
	if err != nil {
		t.Fatalf("Opening the Kafka producer failed: %v", err)
	}
	defer producer.Close()
	for _, fixture := range fixtures {
		key, err := hex.DecodeString(fixture.PartitionBytes)
		if err != nil {
			t.Fatalf("Decoding the Kafka record key of legacy event %s failed: %v", fixture.EventID, err)
		}
		record := &kgo.Record{Key: key, Value: []byte(fixture.Value)}
		if err := producer.ProduceSync(t.Context(), record).FirstErr(); err != nil {
			t.Fatalf("Producing legacy event %s failed: %v", fixture.EventID, err)
		}
	}
}

// requireLegacyLedger requires one ledger row for each fixture event and an
// export of each fixture organization that passes chain verification.
func requireLegacyLedger(t *testing.T, pipeline partsPipeline, fixtures []legacyFixture) {
	t.Helper()
	oldest, latest := fixtures[0].occurredAt, fixtures[0].occurredAt
	organizations := map[uuid.UUID]bool{}
	for _, fixture := range fixtures {
		pipeline.stack().waitForRow(t, fixture.EventID)
		if rows := pipeline.count(t, eventRowsQuery, fixture.EventID); rows != 1 {
			t.Fatalf("The ledger has %d rows for legacy %s event %s instead of 1", rows, fixture.Verb, fixture.EventID)
		}
		if fixture.occurredAt.Before(oldest) {
			oldest = fixture.occurredAt
		}
		if fixture.occurredAt.After(latest) {
			latest = fixture.occurredAt
		}
		organizations[fixture.OrgID] = true
	}
	for orgID := range organizations {
		if err := pipeline.stack().exportAndVerify(t, orgID, oldest.Add(-time.Minute), latest.Add(time.Minute)); err != nil {
			t.Fatalf("The export of organization %s with legacy events failed verification: %v", orgID, err)
		}
	}
}
