package ops

import (
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// planPipelineDeadline bounds the wait for the relay and the consumer to
	// record the plan rows in audit.events.
	planPipelineDeadline = 90 * time.Second
	// planPipelinePoll is how often the relay, the consumer, and the test
	// poll.
	planPipelinePoll = 100 * time.Millisecond
	// planTopicRetention is the retention of a test topic, longer than any
	// test.
	planTopicRetention = time.Hour
)

// planKindsQuery reads the event ID, verb, and outcome of every row of one
// plan from audit.events and from public.ops_outbox. UNION keeps one row of
// an event that both tables contain.
const planKindsQuery = `
SELECT event_id::text, action, coalesce(outcome, '') FROM audit.events WHERE extra->>'plan_id' = $1
UNION
SELECT event_id::text, event->>'verb', coalesce(event->>'outcome', '') FROM public.ops_outbox
 WHERE event->'extra'->>'plan_id' = $1`

// planPipeline is the Kafka broker, the audit topic, and the consumer group
// of one test.
type planPipeline struct {
	brokers string
	topic   string
	group   string
}

// newPlanPipeline returns the shared test broker with a topic and a consumer
// group of this test. It creates the topic through the production function
// that the audit consumer runs at startup, before any relay or consumer
// starts. The relay producer does not ask the broker to create a missing
// topic.
func newPlanPipeline(t *testing.T) planPipeline {
	t.Helper()
	suffix := uuid.NewString()[:8]
	pipeline := planPipeline{brokers: testenv.Kafka(t), topic: "audit.plan-test-" + suffix, group: "tack-plan-test-" + suffix}
	client, err := kgo.NewClient(kgo.SeedBrokers(audit.SplitBrokers(pipeline.brokers)...))
	if err != nil {
		t.Fatalf("open a kafka client to create topic %s: %v", pipeline.topic, err)
	}
	defer client.Close()
	if err := audit.EnsureAuditTopic(t.Context(), client, pipeline.topic, planTopicRetention); err != nil {
		t.Fatalf("create topic %s: %v", pipeline.topic, err)
	}
	return pipeline
}

// configure returns deps with a copy of its configuration that points plan
// close at the pipeline topic, the pipeline consumer group, and the ledger
// at ledgerDSN.
func (p planPipeline) configure(deps dbSQLDeps, ledgerDSN string) dbSQLDeps {
	cfg := *deps.cfg
	cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic, cfg.AuditConsumerGroupID = p.brokers, p.topic, p.group
	cfg.AuditReaderDSN = ledgerDSN
	deps.cfg = &cfg
	return deps
}

// startConsumer runs the real audit consumer of the pipeline group over the
// pipeline topic into the ledger at ledgerDSN until the test ends.
func (p planPipeline) startConsumer(t *testing.T, ledgerDSN string) {
	t.Helper()
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{p.brokers}, Topic: p.topic, GroupID: p.group,
		BatchSize: 32, PollInterval: planPipelinePoll, YugabyteDSN: ledgerDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
}

// startRelay runs the real relay from the operator outbox over pool to the
// pipeline topic. The returned function stops the relay; the test end stops
// it too.
func (p planPipeline) startRelay(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(p.brokers), Topic: p.topic, ClientID: "tack-plan-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the relay producer: %v", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(pool), FoundationDB: nil,
		PollInterval: planPipelinePoll, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("start the relay: %v", err)
	}
	relay.Start(t.Context())
	var once sync.Once
	stop := func() { once.Do(func() { _ = relay.Close() }) }
	t.Cleanup(stop)
	return stop
}

// planKinds counts the rows of planID in audit.events and public.ops_outbox
// by verb and outcome, such as "ops.db_break_glass refused".
func planKinds(t *testing.T, pool *pgxpool.Pool, planID string) map[string]int {
	t.Helper()
	rows, err := pool.Query(t.Context(), planKindsQuery, planID)
	if err != nil {
		t.Fatalf("read the rows of plan %s: %v", planID, err)
	}
	type planKindRow struct {
		EventID string
		Verb    string
		Outcome string
	}
	collected, err := pgx.CollectRows(rows, pgx.RowToStructByPos[planKindRow])
	if err != nil {
		t.Fatalf("read the rows of plan %s: %v", planID, err)
	}
	kinds := map[string]int{}
	for _, row := range collected {
		kinds[row.Verb+" "+row.Outcome]++
	}
	return kinds
}

// waitForLedgerOpenRow polls audit.events until it contains the open row of
// planID. While the relay runs, the open row is in neither public.ops_outbox
// nor audit.events between the broker acknowledgment and the consumer write,
// and a planned statement in that interval is refused for a missing open row.
func waitForLedgerOpenRow(t *testing.T, pool *pgxpool.Pool, planID string) {
	t.Helper()
	deadline := clock.Now().Add(planPipelineDeadline)
	for {
		var count int
		err := pool.QueryRow(t.Context(),
			`SELECT count(*) FROM audit.events WHERE action = $1 AND extra->>'plan_id' = $2`,
			string(audit.VerbOpsDBPlanOpen), planID).Scan(&count)
		if err == nil && count == 1 {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("audit.events open rows of plan %s = %d (%v) after %s, want 1", planID, count, err, planPipelineDeadline)
		}
		time.Sleep(planPipelinePoll)
	}
}

// waitForPlanKinds polls planKinds until it equals want, and fails with the
// last count when planPipelineDeadline passes first.
func waitForPlanKinds(t *testing.T, pool *pgxpool.Pool, planID string, want map[string]int) {
	t.Helper()
	deadline := clock.Now().Add(planPipelineDeadline)
	for {
		kinds := planKinds(t, pool, planID)
		if maps.Equal(kinds, want) {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("rows of plan %s = %v after %s, want %v", planID, kinds, planPipelineDeadline, want)
		}
		time.Sleep(planPipelinePoll)
	}
}
