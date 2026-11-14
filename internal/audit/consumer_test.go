package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// integrationDSN returns the migrated test ledger's superuser DSN.
func integrationDSN(t *testing.T) string {
	t.Helper()
	return testenv.Ledger(t)
}

func newConsumerEnv(t *testing.T) (*pgxpool.Pool, []string, string) {
	t.Helper()
	dsn := integrationDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}

	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, testKafkaTopic))
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}

	t.Cleanup(cluster.Close)

	return pool, cluster.ListenAddrs(), testKafkaTopic
}

func produceEvents(t *testing.T, brokers []string, topic string, events []Event) []uuid.UUID {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		t.Fatalf("producer client: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ids := make([]uuid.UUID, 0, len(events))
	for _, ev := range events {
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		// The consumer reads event_id from the JSON Event, not a Kafka header.
		ids = append(ids, ev.EventID)
		rec := &kgo.Record{
			Topic: topic,
			Value: body,
		}
		if err := cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
			t.Fatalf("produce: %v", err)
		}
	}

	return ids
}

// runConsumerOnce stops the consumer after orgID has at least expect ledger
// rows. Count by org directly; audit.consumer_offsets cannot isolate an org.
func runConsumerOnce(t *testing.T, cfg ConsumerConfig, orgID uuid.UUID, expect int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	consumer, err := NewConsumer(ctx, cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	consumer.Start(ctx)
	deadline := time.After(20 * time.Second)

	for {
		got := countRowsForOrg(t, cfg.YugabyteDSN, orgID)
		if got >= expect {
			break
		}

		select {
		case <-deadline:
			t.Fatalf("waited for %d rows in audit.events; got %d", expect, got)
		case <-time.After(200 * time.Millisecond):
		}
	}

	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// runConsumerUntilOffset stops the consumer after its group records an offset
// of at least want on a topic partition.
func runConsumerUntilOffset(t *testing.T, cfg ConsumerConfig, want int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	consumer, err := NewConsumer(ctx, cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	consumer.Start(ctx)
	pool, err := pgxpool.New(ctx, cfg.YugabyteDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	deadline := time.After(20 * time.Second)

	for {
		var got int64
		_ = pool.QueryRow(
			ctx,
			`
			SELECT coalesce(max("offset"), 0) FROM audit.consumer_offsets
			 WHERE consumer_group = $1 AND topic = $2
		`,
			cfg.GroupID,
			cfg.Topic,
		).Scan(
			&got,
		)
		if got >= want {
			break
		}

		select {
		case <-deadline:
			t.Fatalf("waited for group %s to record offset %d; got %d", cfg.GroupID, want, got)
		case <-time.After(200 * time.Millisecond):
		}
	}

	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func countRowsForOrg(t *testing.T, dsn string, orgID uuid.UUID) int {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return 0
	}
	defer pool.Close()
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM audit.events WHERE org_id = $1`, orgID).Scan(&n)
	if err != nil {
		return 0
	}

	return n
}

func makeReadEvent(orgID uuid.UUID, suffix string) Event {
	return Event{
		EventID: uuid.Must(uuid.NewV7()),
		Verb:    string(VerbNodeRead),
		Actor: Actor{
			Type:      ActorUser,
			ID:        uuid.Must(uuid.NewV7()),
			RequestID: "req-" + suffix,
		},
		Entity: Entity{
			Type: "node",
			ID:   uuid.Must(uuid.NewV7()),
		},
		Context: EventContext{
			OrgID:  orgID,
			Source: SourceMCP,
			Tool:   "tack_get_issue",
		},
		Outcome: OutcomeOK,
		// Use a current timestamp within the partitions migration 002 creates.
		OccurredAt: clock.Now().UTC(),
	}
}

func purgeOrg(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, orgID)
	_, _ = pool.Exec(ctx, `DELETE FROM audit.chain_heads WHERE org_id = $1`, orgID)
}

// TestConsumerProjectsToEvents checks that 100 produced events create ledger
// rows with valid sequence numbers and hash lengths.
func TestConsumerProjectsToEvents(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	const total = 100

	events := make([]Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, makeReadEvent(orgID, strconv.Itoa(i)))
	}

	produceEvents(t, brokers, topic, events)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	runConsumerOnce(
		t,
		ConsumerConfig{
			Brokers:      brokers,
			Topic:        topic,
			GroupID:      groupID,
			BatchSize:    32,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  integrationDSN(t),
		},
		orgID,
		total,
	)

	ctx := context.Background()
	rows, err := pool.Query(
		ctx,
		`
		SELECT seq, prev_hash, row_hash FROM audit.events
		WHERE org_id = $1 ORDER BY shard, seq
	`,
		orgID,
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var count int

	for rows.Next() {
		var seq int64
		var prev, row []byte
		if err := rows.Scan(&seq, &prev, &row); err != nil {
			t.Fatalf("scan: %v", err)
		}

		if seq <= 0 {
			t.Fatalf("seq=%d invalid", seq)
		}

		if len(row) != 32 {
			t.Fatalf("row_hash len=%d", len(row))
		}

		count++
	}

	if count != total {
		t.Fatalf("got %d rows in audit.events; want %d", count, total)
	}
}

// TestConsumerIdempotentOnEventID checks that two groups reading the same 100
// events create exactly 100 ledger rows.
func TestConsumerIdempotentOnEventID(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	const total = 100

	events := make([]Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, makeReadEvent(orgID, strconv.Itoa(i)))
	}

	produceEvents(t, brokers, topic, events)

	groupA := "tack-audit-projector-test-" + uuid.NewString()[:8]
	dsn := integrationDSN(t)
	runConsumerOnce(
		t,
		ConsumerConfig{
			Brokers:      brokers,
			Topic:        topic,
			GroupID:      groupA,
			BatchSize:    32,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  dsn,
		},
		orgID,
		total,
	)

	// Wait until the second group commits past all produced records before
	// checking duplicates. A group that has not consumed them proves nothing.
	groupB := "tack-audit-projector-test-" + uuid.NewString()[:8]
	runConsumerUntilOffset(
		t,
		ConsumerConfig{
			Brokers:      brokers,
			Topic:        topic,
			GroupID:      groupB,
			BatchSize:    32,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  dsn,
		},
		total,
	)

	ctx := context.Background()
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM audit.events WHERE org_id = $1`, orgID).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	if n != total {
		t.Fatalf("got %d rows after second run; want %d (unique index should dedup)", n, total)
	}
}

// TestConsumerOffsetAdvanceIsAtomicWithProjection checks the final row count
// after stopping and restarting the consumer during projection.
func TestConsumerOffsetAdvanceIsAtomicWithProjection(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	const total = 50

	events := make([]Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, makeReadEvent(orgID, strconv.Itoa(i)))
	}

	produceEvents(t, brokers, topic, events)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	dsn := integrationDSN(t)
	cfg := ConsumerConfig{
		Brokers:      brokers,
		Topic:        topic,
		GroupID:      groupID,
		BatchSize:    8,
		PollInterval: 100 * time.Millisecond,
		YugabyteDSN:  dsn,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	first, err := NewConsumer(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("NewConsumer first: %v", err)
	}

	first.Start(ctx)
	time.Sleep(500 * time.Millisecond)
	_ = first.Close()
	cancel()

	runConsumerOnce(t, cfg, orgID, total)

	c := context.Background()
	var n int
	err = pool.QueryRow(c, `SELECT count(*) FROM audit.events WHERE org_id = $1`, orgID).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	if n != total {
		t.Fatalf("got %d rows; want exactly %d (no missing, no double-projection)", n, total)
	}
}

// TestConsumerNotarizerSigns checks at least two notarizations with valid
// Ed25519 signatures and the expected signing key and host.
func TestConsumerNotarizerSigns(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM audit.notarizations WHERE org_id = $1`, orgID)
	})

	keyPath := writeTempSigningKey(t)

	events := []Event{makeReadEvent(orgID, "notarize")}
	produceEvents(t, brokers, topic, events)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	cfg := ConsumerConfig{
		Brokers:         brokers,
		Topic:           topic,
		GroupID:         groupID,
		BatchSize:       8,
		PollInterval:    100 * time.Millisecond,
		YugabyteDSN:     integrationDSN(t),
		SigningKeyPath:  keyPath,
		NotarizerPeriod: 1 * time.Second,
		SigningHost:     "test-guest",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	consumer, err := NewConsumer(ctx, cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	consumer.Start(ctx)

	deadline := time.After(7 * time.Second)

	for {
		var n int
		_ = pool.QueryRow(
			ctx,
			`SELECT count(*) FROM audit.notarizations WHERE org_id = $1`,
			orgID,
		).Scan(
			&n,
		)
		if n >= 2 {
			break
		}

		select {
		case <-deadline:
			t.Fatalf("notarizer did not produce >=2 rows; got %d", n)
		case <-time.After(200 * time.Millisecond):
		}
	}

	rows, err := pool.Query(
		ctx,
		`
		SELECT merkle_root, signature, signing_key, signing_host
		  FROM audit.notarizations WHERE org_id = $1
	`,
		orgID,
	)
	if err != nil {
		t.Fatalf("query notarizations: %v", err)
	}
	defer rows.Close()

	pubKey := loadPublicKey(t, keyPath)
	var seen int

	for rows.Next() {
		var root, sig []byte
		var keyID, host string
		if err := rows.Scan(&root, &sig, &keyID, &host); err != nil {
			t.Fatalf("scan: %v", err)
		}

		if !ed25519.Verify(pubKey, root, sig) {
			t.Fatalf("invalid signature for key_id=%s", keyID)
		}

		if keyID != KeyIdentifier(pubKey) || host != "test-guest" {
			t.Fatalf(
				"row stamped key_id=%s host=%q, want %s from test-guest",
				keyID,
				host,
				KeyIdentifier(pubKey),
			)
		}

		seen++
	}

	if seen < 2 {
		t.Fatalf("verified %d signatures, want >=2", seen)
	}

	_ = consumer.Close()
}

// TestConsumerHandlesMalformedPayload checks that invalid JSON creates a dead
// letter without preventing projection of the next event.
func TestConsumerHandlesMalformedPayload(t *testing.T) {
	pool, brokers, topic := newConsumerEnv(t)
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() {
		purgeOrg(t, pool, orgID)
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM audit.events_dlq WHERE topic = $1`, topic)
	})

	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatalf("producer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cl.ProduceSync(
		ctx,
		&kgo.Record{
			Topic: topic,
			Value: []byte("{not valid json"),
		},
	).FirstErr(); err != nil {
		t.Fatalf("produce malformed: %v", err)
	}

	cl.Close()

	events := []Event{makeReadEvent(orgID, "after-malformed")}
	produceEvents(t, brokers, topic, events)

	groupID := "tack-audit-projector-test-" + uuid.NewString()[:8]
	runConsumerOnce(
		t,
		ConsumerConfig{
			Brokers:      brokers,
			Topic:        topic,
			GroupID:      groupID,
			BatchSize:    8,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  integrationDSN(t),
		},
		orgID,
		1,
	)

	var dlqCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM audit.events_dlq WHERE topic = $1`, topic).Scan(
		&dlqCount,
	)
	if err != nil {
		t.Fatalf("dlq count: %v", err)
	}

	if dlqCount < 1 {
		t.Fatalf("expected dlq row; got %d", dlqCount)
	}
}

func writeTempSigningKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-test.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return path
}

func loadPublicKey(t *testing.T, path string) ed25519.PublicKey {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("not pem")
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("not ed25519")
	}

	return priv.Public().(ed25519.PublicKey)
}
