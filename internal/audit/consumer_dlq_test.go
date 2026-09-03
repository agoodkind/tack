package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// deadLetterTestPartition is the hand-made partition the test creates once
// the refusal has been observed. Its week sits five years out, past anything
// the partition manager premakes, so the refusal keeps happening year after
// year and the created partition never collides with a managed one.
const deadLetterTestPartition = "audit.events_test_dlq_far_future"

// deadLetterTestWeek returns the start of the week five years from now and
// an instant inside it.
func deadLetterTestWeek() (time.Time, time.Time) {
	start := time.Now().UTC().AddDate(5, 0, 0).Truncate(24 * time.Hour)
	return start, start.Add(36 * time.Hour)
}

func countDeadLetters(t *testing.T, pool *pgxpool.Pool, topic string) (int, int, string) {
	t.Helper()
	var rows, attempts int
	var errText string
	err := pool.QueryRow(context.Background(), `
		SELECT count(*), coalesce(max(attempt_count), 0), coalesce(max(error), '')
		  FROM audit.events_dlq WHERE topic = $1
	`, topic).Scan(&rows, &attempts, &errText)
	if err != nil {
		t.Fatalf("count dead letters: %v", err)
	}
	return rows, attempts, errText
}

// waitForDeadLetters polls until the table holds the expected row count with
// at least the expected attempts, or fails after the deadline. A replay's
// records can land in separate polls, so the ledger count alone is not the
// end of the run.
func waitForDeadLetters(t *testing.T, pool *pgxpool.Pool, topic string, wantRows, wantAttempts int) (int, int, string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rows, attempts, errText := countDeadLetters(t, pool, topic)
		if rows == wantRows && attempts >= wantAttempts {
			return rows, attempts, errText
		}
		if time.Now().After(deadline) {
			return rows, attempts, errText
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// writerLoginDSN creates a throwaway login that inherits audit_writer, the
// role the deployed consumer runs as, and returns the test DSN rewritten to
// connect as it. The consumer under test then holds exactly the privileges
// migration 010 grants and nothing the admin role has, so a missing grant
// fails here rather than on a deployment.
func writerLoginDSN(t *testing.T, admin *pgxpool.Pool, adminDSN string) string {
	t.Helper()
	ctx := context.Background()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("login suffix: %v", err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("login secret: %v", err)
	}
	login := "tack_test_dlq_writer_" + hex.EncodeToString(suffix)
	encodedSecret := hex.EncodeToString(secret)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+login+" LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '"+encodedSecret+"'"); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+login) })
	if _, err := admin.Exec(ctx, "GRANT audit_writer TO "+login); err != nil {
		t.Fatalf("grant audit_writer to %s: %v", login, err)
	}
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, encodedSecret)
	return parsed.String()
}

func produceRaw(t *testing.T, brokers []string, topic string, value []byte, headers []kgo.RecordHeader) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		t.Fatalf("producer client: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cl.ProduceSync(ctx, &kgo.Record{Topic: topic, Value: value, Headers: headers}).FirstErr(); err != nil {
		t.Fatalf("produce: %v", err)
	}
}

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
