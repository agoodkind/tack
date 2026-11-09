package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"expvar"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// deadLetterTestPartition identifies the partition that the test creates
// after observing an insert refusal. The test uses dates five years ahead,
// beyond the partition manager's prepared range, to avoid a managed
// partition.
const deadLetterTestPartition = "audit.events_test_dlq_far_future"

// deadLetterTestWeek returns midnight UTC on the date five years ahead and
// an instant 36 hours later for the test partition.
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

// waitForDeadLetters waits for the expected row count and for at least one
// row to meet the expected attempt count. It returns the last observation at
// the deadline. Replay records can arrive in separate polls. Ledger counts
// do not establish replay completion.
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

// writerLoginDSN creates a temporary login that inherits audit_writer, the
// deployed consumer's role, and returns a test DSN for that login. The test
// consumer uses the writer grants from migration 011 without the admin
// role's privileges. Missing grants fail the test before deployment.
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

// consumerErrorCount reads the process-wide count of records in failed
// batches from the counter that operators inspect.
func consumerErrorCount(t *testing.T) int64 {
	t.Helper()
	processed, ok := expvar.Get("tack_audit_consumer_processed_total").(*expvar.Map)
	if !ok {
		t.Fatalf("the consumer's processed counter is not published")
	}
	errored, ok := processed.Get("error").(*expvar.Int)
	if !ok {
		return 0
	}
	return errored.Value()
}

// waitUntil polls the condition until it holds or the deadline passes.
func waitUntil(t *testing.T, limit time.Duration, failure string, condition func() bool) {
	t.Helper()
	deadline := time.After(limit)
	for !condition() {
		select {
		case <-deadline:
			t.Fatal(failure)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// loginOfDSN returns the login name a DSN connects as.
func loginOfDSN(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse the DSN: %v", err)
	}
	return parsed.User.Username()
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
