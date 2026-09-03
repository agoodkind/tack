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

// deadLetterTestPartition covers one week in 2028 that no premade partition
// reaches, so an event dated inside it is refused by the ledger exactly the
// way the 2026-07-06 events were, and creating the partition afterwards is
// the cause clearing.
const (
	deadLetterTestPartition = "audit.events_test_dlq_2028w10"
	deadLetterTestFrom      = "2028-03-06"
	deadLetterTestTo        = "2028-03-13"
)

var deadLetterTestOccurredAt = time.Date(2028, time.March, 8, 12, 0, 0, 0, time.UTC)

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
// replay with its attempt counted.
func TestConsumerDeadLettersARefusedInsertAndReplaysIt(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		purgeOrg(t, pool, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events_dlq WHERE topic = $1`, topic)
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS `+deadLetterTestPartition)
	})
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS `+deadLetterTestPartition)

	refused := makeReadEvent(orgID, "refused")
	refused.OccurredAt = deadLetterTestOccurredAt
	accepted := makeReadEvent(orgID, "accepted")
	produceEvents(t, brokers, topic, []Event{refused, accepted})
	produceRaw(t, brokers, topic, []byte(`{"not":"an event"`), nil)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	cfg := ConsumerConfig{
		Brokers: brokers, Topic: topic, GroupID: groupID,
		BatchSize: 32, PollInterval: 100 * time.Millisecond,
		YugabyteDSN: writerLoginDSN(t, pool, os.Getenv("AUDIT_CONSUMER_TEST_DSN")),
	}
	runConsumerOnce(t, cfg, 1)

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
	if _, err := pool.Exec(ctx, `CREATE TABLE `+deadLetterTestPartition+` PARTITION OF audit.events FOR VALUES FROM ('`+deadLetterTestFrom+`') TO ('`+deadLetterTestTo+`')`); err != nil {
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
	runConsumerOnce(t, cfg, 2)

	rows, attempts, remainingErr := countDeadLetters(t, pool, topic)
	if rows != 1 {
		t.Fatalf("dead letters after the replay = %d, want only the malformed payload", rows)
	}
	// At least one: the replay itself. A consumer that resumes from the
	// topic's start counts the original record's second failure too, and
	// every failed projection of one key is an attempt worth counting.
	if attempts < 1 || !strings.Contains(remainingErr, "malformed") {
		t.Fatalf("remaining dead letter attempts = %d error = %q, want the replay counted on the malformed payload", attempts, remainingErr)
	}
	var landed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit.events WHERE event_id = $1`, refused.EventID).Scan(&landed); err != nil {
		t.Fatalf("count the replayed event: %v", err)
	}
	if landed != 1 {
		t.Fatalf("the replayed event is in the ledger %d times, want 1", landed)
	}
}
