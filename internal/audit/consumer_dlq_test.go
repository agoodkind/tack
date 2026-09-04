package audit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
)

// TestConsumerDeadLettersARefusedInsertAndReplaysIt is TACK-336's consumer
// half end to end. An event the ledger refuses (dated into a week with no
// partition) lands in the dead-letter table instead of failing the batch, the
// batch's other event still commits, and the offset advances past both. Once
// the partition exists, a replay through the topic lands the event and the
// consumer deletes the row. A payload the decoder rejects stays after a
// replay with exactly one attempt counted: the second consumer, in the same
// group, resumes from the committed offset and never re-reads the original.
func TestConsumerDeadLettersARefusedInsertAndReplaysIt(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	weekStart, occurredAt := deadLetterTestWeek()
	t.Cleanup(func() {
		purgeOrg(t, pool, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events_dlq WHERE topic = $1`, topic)
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS `+deadLetterTestPartition)
	})
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS `+deadLetterTestPartition)

	refused := makeReadEvent(orgID, "refused")
	refused.OccurredAt = occurredAt
	accepted := makeReadEvent(orgID, "accepted")
	produceEvents(t, brokers, topic, []Event{refused, accepted})
	produceRaw(t, brokers, topic, []byte(`{"not":"an event"`), nil)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	cfg := ConsumerConfig{
		Brokers: brokers, Topic: topic, GroupID: groupID,
		BatchSize: 32, PollInterval: 100 * time.Millisecond,
		YugabyteDSN: writerLoginDSN(t, pool, os.Getenv("AUDIT_CONSUMER_TEST_DSN")),
	}
	runConsumerOnce(t, cfg, orgID, 1)

	rows, _, refusedErr := countDeadLetters(t, pool, topic)
	if rows != 2 {
		t.Fatalf("dead letters after the first run = %d, want the refused insert and the malformed payload", rows)
	}
	if !strings.Contains(refusedErr, "no partition") && !strings.Contains(refusedErr, "malformed") {
		t.Fatalf("dead-letter error = %q, want the ledger's refusal or the decode failure", refusedErr)
	}
	var offsetRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit.consumer_offsets WHERE consumer_group = $1`, groupID).Scan(&offsetRows); err != nil {
		t.Fatalf("offsets: %v", err)
	}
	if offsetRows != 1 {
		t.Fatalf("the batch with a refused insert must still commit its offset; offset rows = %d", offsetRows)
	}

	// The cause clears: the week gains a partition. Replay everything.
	// DDL takes no bind parameters; the bounds are dates this test computed.
	partitionDDL := `CREATE TABLE ` + deadLetterTestPartition + ` PARTITION OF audit.events FOR VALUES FROM ('` +
		weekStart.Format("2006-01-02") + `') TO ('` + weekStart.AddDate(0, 0, 7).Format("2006-01-02") + `')`
	if _, err := pool.Exec(ctx, partitionDDL); err != nil {
		t.Fatalf("create the missing partition: %v", err)
	}
	reader := &Reader{pool: pool}
	letters, err := reader.ListDeadLetters(ctx, 10)
	if err != nil {
		t.Fatalf("list dead letters: %v", err)
	}
	for _, letter := range letters {
		produceRaw(t, brokers, topic, letter.Payload, []kgo.RecordHeader{{Key: dlqReplayHeader, Value: []byte(letter.Key.String())}})
	}
	runConsumerOnce(t, cfg, orgID, 2)

	rows, attempts, remainingErr := waitForDeadLetters(t, pool, topic, 1, 1)
	if rows != 1 {
		t.Fatalf("dead letters after the replay = %d, want only the malformed payload", rows)
	}
	if attempts != 1 || !strings.Contains(remainingErr, "malformed") {
		t.Fatalf("remaining dead letter attempts = %d error = %q, want exactly the replay counted on the malformed payload", attempts, remainingErr)
	}
	var landed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit.events WHERE event_id = $1`, refused.EventID).Scan(&landed); err != nil {
		t.Fatalf("count the replayed event: %v", err)
	}
	if landed != 1 {
		t.Fatalf("the replayed event is in the ledger %d times, want 1", landed)
	}
}

// TestConsumerIgnoresAnUnparseableReplayHeader pins that a record carrying a
// replay header the consumer cannot read is projected as a fresh record and
// the partition keeps moving, rather than the batch failing on every poll.
func TestConsumerIgnoresAnUnparseableReplayHeader(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	body, err := MarshalEvent(makeReadEvent(orgID, "hostile-header"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	produceRaw(t, brokers, topic, body, []kgo.RecordHeader{{Key: dlqReplayHeader, Value: []byte("not/a/valid/key/at/all")}})
	produceEvents(t, brokers, topic, []Event{makeReadEvent(orgID, "after")})

	runConsumerOnce(t, ConsumerConfig{
		Brokers: brokers, Topic: topic, GroupID: "tack-audit-projector-test-" + uuid.NewString()[:8],
		BatchSize: 8, PollInterval: 100 * time.Millisecond,
		YugabyteDSN: os.Getenv("AUDIT_CONSUMER_TEST_DSN"),
	}, orgID, 2)
}
