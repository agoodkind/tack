package audit

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

// clickHouseTestDSNEnv names a reachable ClickHouse DSN. The test below
// projects events through the consumer into both Yugabyte and ClickHouse, then
// exercises the QueryRouter against both real stores. It skips when unset (and
// when AUDIT_CONSUMER_TEST_DSN is unset, via newConsumerEnv).
const clickHouseTestDSNEnv = "AUDIT_CLICKHOUSE_TEST_DSN"

func waitRouterCount(ctx context.Context, t *testing.T, router *QueryRouter, f QueryFilter, want int) {
	t.Helper()
	var last int
	for i := 0; i < 50; i++ {
		rows, err := router.Query(ctx, f)
		if err != nil {
			t.Fatalf("router query: %v", err)
		}
		last = len(rows)
		if last >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("router query reached %d rows, want %d", last, want)
}

// TestClickHouseProjectionAndRouting validates the analytical read tier: the
// consumer projects to audit.events_olap, the ClickHouseReader reads it back,
// and the QueryRouter sends recent windows to ClickHouse and older windows to
// Yugabyte. Emptying Yugabyte after both stores hold the rows proves the
// routing: the recent window still returns them (ClickHouse) while the old
// window returns none (the now-empty Yugabyte).
func TestClickHouseProjectionAndRouting(t *testing.T) {
	chDSN := os.Getenv(clickHouseTestDSNEnv)
	if chDSN == "" {
		t.Skipf("set %s to run", clickHouseTestDSNEnv)
	}
	pool, brokers, topic := newConsumerEnv(t)
	ybDSN := os.Getenv("AUDIT_CONSUMER_TEST_DSN")
	ctx := context.Background()
	orgID := uuid.Must(uuid.NewV7())
	t.Cleanup(func() { purgeOrg(t, pool, orgID) })

	const total = 8
	events := make([]Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, makeReadEvent(orgID, strconv.Itoa(i)))
	}
	produceEvents(t, brokers, topic, events)
	runConsumerOnce(t, ConsumerConfig{
		Brokers:       brokers,
		Topic:         topic,
		GroupID:       "ch-test-" + uuid.NewString()[:8],
		BatchSize:     16,
		PollInterval:  100 * time.Millisecond,
		YugabyteDSN:   ybDSN,
		ClickHouseDSN: chDSN,
	}, total)

	ybReader, err := NewReader(ctx, ybDSN)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	t.Cleanup(ybReader.Close)
	chReader, err := NewClickHouseReader(ctx, chDSN)
	if err != nil {
		t.Fatalf("NewClickHouseReader: %v", err)
	}
	t.Cleanup(chReader.Close)
	router := NewQueryRouter(ybReader, chReader, time.Hour)

	now := clock.Now().UTC()
	recent := QueryFilter{
		OrgID: orgID, Oldest: now.Add(-30 * time.Minute), Latest: now.Add(30 * time.Minute),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 1000,
	}
	old := QueryFilter{
		OrgID: orgID, Oldest: now.Add(-3 * time.Hour), Latest: now.Add(30 * time.Minute),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 1000,
	}

	// ClickHouse write is best effort after the commit; allow it to catch up.
	waitRouterCount(ctx, t, router, recent, total)

	oldRows, err := router.Query(ctx, old)
	if err != nil {
		t.Fatalf("old query: %v", err)
	}
	if len(oldRows) != total {
		t.Fatalf("old window (Yugabyte) returned %d, want %d", len(oldRows), total)
	}

	// Empty Yugabyte, keep ClickHouse, and re-query. The recent window must
	// still find every row (ClickHouse), and the old window must find none
	// (Yugabyte), which only holds if the router routed each correctly.
	if _, derr := pool.Exec(ctx, `DELETE FROM audit.events WHERE org_id = $1`, orgID); derr != nil {
		t.Fatalf("delete yugabyte rows: %v", derr)
	}
	recentRows, err := router.Query(ctx, recent)
	if err != nil {
		t.Fatalf("recent query after delete: %v", err)
	}
	if len(recentRows) != total {
		t.Fatalf("recent window (ClickHouse) after Yugabyte delete returned %d, want %d", len(recentRows), total)
	}
	oldAfter, err := router.Query(ctx, old)
	if err != nil {
		t.Fatalf("old query after delete: %v", err)
	}
	if len(oldAfter) != 0 {
		t.Fatalf("old window (Yugabyte) after delete returned %d, want 0", len(oldAfter))
	}
}
