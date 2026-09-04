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

// TestConsumerSurvivesHostileRecords pins two records that must not stop a
// partition: one with no value at all (a tombstone), which is dead-lettered
// as an empty payload, and one carrying a replay header the consumer cannot
// read, which is projected as a fresh record. The events after them land.
func TestConsumerSurvivesHostileRecords(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		purgeOrg(t, pool, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events_dlq WHERE topic = $1`, topic)
	})

	produceRaw(t, brokers, topic, nil, nil)
	body, err := MarshalEvent(makeReadEvent(orgID, "hostile-header"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	produceRaw(t, brokers, topic, body, []kgo.RecordHeader{{Key: dlqReplayHeader, Value: []byte("not/a/valid/key/at/all")}})
	produceEvents(t, brokers, topic, []Event{makeReadEvent(orgID, "after")})

	runConsumerOnce(t, ConsumerConfig{
		Brokers: brokers, Topic: topic, GroupID: "tack-audit-projector-test-" + uuid.NewString()[:8],
		BatchSize: 8, PollInterval: 100 * time.Millisecond,
		YugabyteDSN: writerLoginDSN(t, pool, os.Getenv("AUDIT_CONSUMER_TEST_DSN")),
	}, orgID, 2)

	var rows, payloadBytes int
	err = pool.QueryRow(ctx, `SELECT count(*), coalesce(max(length(payload)), 0) FROM audit.events_dlq WHERE topic = $1`, topic).Scan(&rows, &payloadBytes)
	if err != nil {
		t.Fatalf("read the dead letters: %v", err)
	}
	if rows != 1 || payloadBytes != 0 {
		t.Fatalf("dead letters = %d with a payload of %d bytes, want the one empty tombstone", rows, payloadBytes)
	}
}

// TestConsumerRewindsAFailedBatch pins that a batch failing for a deployment
// fault (here the writer login losing every privilege) commits nothing and
// re-fetches the same records once the fault clears, so no offset is ever
// committed past records the ledger never saw.
func TestConsumerRewindsAFailedBatch(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	dsn := writerLoginDSN(t, pool, os.Getenv("AUDIT_CONSUMER_TEST_DSN"))
	login := loginOfDSN(t, dsn)
	if _, err := pool.Exec(ctx, "ALTER ROLE "+login+" NOINHERIT"); err != nil {
		t.Fatalf("take the login's privileges away: %v", err)
	}
	produceEvents(t, brokers, topic, []Event{makeReadEvent(orgID, "first"), makeReadEvent(orgID, "second")})

	cfg := ConsumerConfig{
		Brokers: brokers, Topic: topic, GroupID: "tack-audit-projector-test-" + uuid.NewString()[:8],
		BatchSize: 8, PollInterval: 100 * time.Millisecond, YugabyteDSN: dsn,
	}
	runCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	consumer, err := NewConsumer(runCtx, cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	consumer.Start(runCtx)
	time.Sleep(3 * time.Second)
	if got := countRowsForOrg(t, os.Getenv("AUDIT_CONSUMER_TEST_DSN"), orgID); got != 0 {
		t.Fatalf("a batch the login could not write landed %d rows", got)
	}
	if _, err := pool.Exec(ctx, "ALTER ROLE "+login+" INHERIT"); err != nil {
		t.Fatalf("give the login's privileges back: %v", err)
	}
	deadline := time.After(30 * time.Second)
	for countRowsForOrg(t, os.Getenv("AUDIT_CONSUMER_TEST_DSN"), orgID) < 2 {
		select {
		case <-deadline:
			t.Fatalf("the failed batch was never re-fetched after the fault cleared")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var committed int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(max("offset"), -1) FROM audit.consumer_offsets WHERE consumer_group = $1`, cfg.GroupID).Scan(&committed); err != nil {
		t.Fatalf("offsets: %v", err)
	}
	if committed < 2 {
		t.Fatalf("committed offset = %d after both records landed, want at least 2", committed)
	}
}
