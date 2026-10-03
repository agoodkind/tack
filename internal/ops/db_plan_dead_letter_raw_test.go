package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// TestDBPlanCloseFailsOnAnUndecodableDeadLetter opens a plan, then produces a
// record that is not JSON and contains the plan ID to the audit topic. The
// audit consumer cannot decode the record and writes it to audit.events_dlq.
// Close cannot rule the record out of the plan: it fails, mails that the
// summary is incomplete, and writes no close row.
func TestDBPlanCloseFailsOnAnUndecodableDeadLetter(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	mail := mailpitFor(t)
	pool := testenv.LedgerPool(t, ledgerDSN)
	pipeline := newPlanPipeline(t, pool, ledgerDSN)
	pipeline.startConsumer(t, ledgerDSN)
	pipeline.startRelay(t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM audit.events_dlq WHERE topic = $1`, pipeline.topic)
	})
	deps := pipeline.configure(planDeps(t, pool, ledgerDSN, mail.Msmtprc, planAgentFlags("session-plan-w")))
	opened, err := openPlan(t, deps, writePlanFile(t, "select 1"), "plan test w "+uuid.NewString()[:8], "1h")
	if err != nil {
		t.Fatalf("plan open: %v", err)
	}
	deleteOutboxRowsAfterTest(t, pool, planRowsFilter(opened.PlanID))
	produceUndecodableRecord(t, pipeline, "not an audit event for plan "+opened.PlanID)
	waitForDeadLetter(t, pool, pipeline.topic, opened.PlanID)

	_, err = closePlan(t, deps, opened.PlanID, "postcheck w")
	if err == nil || !strings.Contains(err.Error(), "audit.events_dlq has 1 rows of plan "+opened.PlanID) {
		t.Fatalf("plan close = %v, want the undecodable dead letter to fail the close", err)
	}
	mailWithSubject(t, requireMailCount(t, mail, 2), "summary of plan "+opened.PlanID+" is incomplete")
	if kinds := planKinds(t, pool, opened.PlanID); kinds["ops.db_plan_close ok"] != 0 {
		t.Fatalf("rows of plan %s = %v, want no close row", opened.PlanID, kinds)
	}
}

// produceUndecodableRecord produces value to the pipeline topic through a
// franz-go client on the test broker.
func produceUndecodableRecord(t *testing.T, pipeline planPipeline, value string) {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(audit.SplitBrokers(pipeline.brokers)...))
	if err != nil {
		t.Fatalf("open a kafka client for topic %s: %v", pipeline.topic, err)
	}
	defer client.Close()
	record := &kgo.Record{Topic: pipeline.topic, Value: []byte(value)}
	if err := client.ProduceSync(t.Context(), record).FirstErr(); err != nil {
		t.Fatalf("produce the undecodable record to topic %s: %v", pipeline.topic, err)
	}
}

// waitForDeadLetter polls audit.events_dlq through pool until topic has a row
// that contains planID.
func waitForDeadLetter(t *testing.T, pool *pgxpool.Pool, topic, planID string) {
	t.Helper()
	deadline := clock.Now().Add(planPipelineDeadline)
	for {
		var letters int
		err := pool.QueryRow(t.Context(),
			`SELECT count(*) FROM audit.events_dlq WHERE topic = $1 AND position(convert_to($2, 'UTF8') IN payload) > 0`,
			topic, planID).Scan(&letters)
		if err == nil && letters > 0 {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("audit.events_dlq rows of plan %s in topic %s = %d (%v) after %s, want one", planID, topic, letters, err, planPipelineDeadline)
		}
		time.Sleep(planPipelinePoll)
	}
}
