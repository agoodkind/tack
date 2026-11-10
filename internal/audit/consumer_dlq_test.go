package audit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/testenv"
)

// TestConsumerDeadLettersARefusedInsertAndReplaysIt checks the consumer
// recovery for TACK-336. An event outside every weekly partition enters the
// dead-letter table, the other event commits, and the offset advances past
// both. After the test creates the partition, replay inserts the event and
// deletes its dead-letter row. A malformed payload remains after replay
// with exactly one counted attempt. The second consumer uses the same
// group and resumes at the committed offset without rereading the original.
func TestConsumerDeadLettersARefusedInsertAndReplaysIt(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	weekStart, occurredAt := deadLetterTestWeek()
	t.Cleanup(func() {
		purgeOrg(t, pool, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events_dlq WHERE topic = $1`, topic)
		testenv.ExecAsRole(
			t,
			pool,
			migration.MigratorRole,
			`DROP TABLE IF EXISTS `+deadLetterTestPartition,
		)
	})
	testenv.ExecAsRole(
		t,
		pool,
		migration.MigratorRole,
		`DROP TABLE IF EXISTS `+deadLetterTestPartition,
	)

	refused := makeReadEvent(orgID, "refused")
	refused.OccurredAt = occurredAt
	accepted := makeReadEvent(orgID, "accepted")
	produceEvents(t, brokers, topic, []Event{refused, accepted})
	produceRaw(t, brokers, topic, []byte(`{"not":"an event"`), nil)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	cfg := ConsumerConfig{
		Brokers:      brokers,
		Topic:        topic,
		GroupID:      groupID,
		BatchSize:    32,
		PollInterval: 100 * time.Millisecond,
		YugabyteDSN:  writerLoginDSN(t, pool, integrationDSN(t)),
	}
	runConsumerOnce(t, cfg, orgID, 1)

	rows, _, refusedErr := countDeadLetters(t, pool, topic)
	if rows != 2 {
		t.Fatalf(
			"dead letters after the first run = %d, want the refused insert and the malformed payload",
			rows,
		)
	}

	if !strings.Contains(refusedErr, "no partition") && !strings.Contains(refusedErr, "malformed") {
		t.Fatalf(
			"dead-letter error = %q, want the ledger's refusal or the decode failure",
			refusedErr,
		)
	}

	var offsetRows int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM audit.consumer_offsets WHERE consumer_group = $1`,
		groupID,
	).Scan(
		&offsetRows,
	); err != nil {
		t.Fatalf("offsets: %v", err)
	}

	if offsetRows != 1 {
		t.Fatalf(
			"the batch with a refused insert must still commit its offset; offset rows = %d",
			offsetRows,
		)
	}

	// The test creates the missing weekly partition before replaying all dead
	// letters. DDL does not accept bind parameters. The partition bounds use
	// dates that this test computed.
	partitionDDL := `CREATE TABLE ` + deadLetterTestPartition + ` PARTITION OF audit.events FOR VALUES FROM ('` +
		weekStart.Format("2006-01-02") + `') TO ('` + weekStart.AddDate(0, 0, 7).Format(
		"2006-01-02",
	) + `')`
	// The schema guard of migration 019 refuses this statement from any role
	// except tack_migrator.
	testenv.ExecAsRole(t, pool, migration.MigratorRole, partitionDDL)
	reader := openTestReader(t, pool)
	letters, err := reader.ListDeadLetters(ctx, 10)
	if err != nil {
		t.Fatalf("list dead letters: %v", err)
	}

	for _, letter := range letters {
		produceRaw(
			t,
			brokers,
			topic,
			letter.Payload,
			[]kgo.RecordHeader{{Key: dlqReplayHeader, Value: []byte(letter.Key.String())}},
		)
	}

	runConsumerOnce(t, cfg, orgID, 2)

	rows, attempts, remainingErr := waitForDeadLetters(t, pool, topic, 1, 1)
	if rows != 1 {
		t.Fatalf("dead letters after the replay = %d, want only the malformed payload", rows)
	}

	if attempts != 1 || !strings.Contains(remainingErr, "malformed") {
		t.Fatalf(
			"remaining dead letter attempts = %d error = %q, want exactly the replay counted on the malformed payload",
			attempts,
			remainingErr,
		)
	}

	var landed int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM audit.events WHERE event_id = $1`,
		refused.EventID,
	).Scan(
		&landed,
	); err != nil {
		t.Fatalf("count the replayed event: %v", err)
	}

	if landed != 1 {
		t.Fatalf("the replayed event is in the ledger %d times, want 1", landed)
	}
}

// TestConsumerSurvivesHostileRecords checks that an empty tombstone enters
// the dead-letter table and that an unreadable replay header permits fresh
// record processing. The consumer also inserts the following events.
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

	produceRaw(
		t,
		brokers,
		topic,
		body,
		[]kgo.RecordHeader{{Key: dlqReplayHeader, Value: []byte("not/a/valid/key/at/all")}},
	)
	produceEvents(t, brokers, topic, []Event{makeReadEvent(orgID, "after")})

	runConsumerOnce(
		t,
		ConsumerConfig{
			Brokers:      brokers,
			Topic:        topic,
			GroupID:      "tack-audit-projector-test-" + uuid.NewString()[:8],
			BatchSize:    8,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  writerLoginDSN(t, pool, integrationDSN(t)),
		},
		orgID,
		2,
	)

	var rows, payloadBytes int
	err = pool.QueryRow(
		ctx,
		`SELECT count(*), coalesce(max(length(payload)), 0) FROM audit.events_dlq WHERE topic = $1`,
		topic,
	).Scan(
		&rows,
		&payloadBytes,
	)
	if err != nil {
		t.Fatalf("read the dead letters: %v", err)
	}

	if rows != 1 || payloadBytes != 0 {
		t.Fatalf(
			"dead letters = %d with a payload of %d bytes, want the one empty tombstone",
			rows,
			payloadBytes,
		)
	}
}

// TestConsumerRewindsAFailedBatch removes the writer login's inherited
// privileges to simulate a deployment fault. The batch does not commit, and
// the consumer fetches the same records after privileges return. Offsets do
// not advance past events that the ledger has not recorded.
func TestConsumerRewindsAFailedBatch(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	dsn := writerLoginDSN(t, pool, integrationDSN(t))
	login := loginOfDSN(t, dsn)
	if _, err := pool.Exec(ctx, "ALTER ROLE "+login+" NOINHERIT"); err != nil {
		t.Fatalf("take the login's privileges away: %v", err)
	}

	produceEvents(
		t,
		brokers,
		topic,
		[]Event{makeReadEvent(orgID, "first"), makeReadEvent(orgID, "second")},
	)

	cfg := ConsumerConfig{
		Brokers:      brokers,
		Topic:        topic,
		GroupID:      "tack-audit-projector-test-" + uuid.NewString()[:8],
		BatchSize:    8,
		PollInterval: 100 * time.Millisecond,
		YugabyteDSN:  dsn,
	}
	// The 70-second consumer context exceeds the combined 20-second and
	// 30-second waits below.
	runCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	consumer, err := NewConsumer(runCtx, cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	t.Cleanup(func() { _ = consumer.Close() })
	failedBefore := consumerErrorCount(t)
	consumer.Start(runCtx)
	// The test restores privileges after the consumer fetches and fails the
	// batch. This order distinguishes a second fetch from a delayed first
	// fetch.
	waitUntil(
		t,
		20*time.Second,
		"the consumer never failed a batch",
		func() bool {
			return consumerErrorCount(t) > failedBefore
		},
	)
	if got := countRowsForOrg(t, integrationDSN(t), orgID); got != 0 {
		t.Fatalf("a batch the login could not write landed %d rows", got)
	}

	if _, err := pool.Exec(ctx, "ALTER ROLE "+login+" INHERIT"); err != nil {
		t.Fatalf("give the login's privileges back: %v", err)
	}

	waitUntil(
		t,
		30*time.Second,
		"the failed batch was never re-fetched after the fault cleared",
		func() bool {
			return countRowsForOrg(t, integrationDSN(t), orgID) >= 2
		},
	)
	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var committed int64
	if err := pool.QueryRow(
		ctx,
		`SELECT coalesce(max("offset"), -1) FROM audit.consumer_offsets WHERE consumer_group = $1`,
		cfg.GroupID,
	).Scan(
		&committed,
	); err != nil {
		t.Fatalf("offsets: %v", err)
	}

	if committed < 2 {
		t.Fatalf("committed offset = %d after both records landed, want at least 2", committed)
	}
}
