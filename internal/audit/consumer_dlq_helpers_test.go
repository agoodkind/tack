package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"testing"
	"time"

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
// migration 011 grants and nothing the admin role has, so a missing grant
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
