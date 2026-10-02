package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// consumedEvents is the number of events the running consumer projects.
	consumedEvents = 20
	// waitingEvents is the number of events the test produces after the
	// consumer stops.
	waitingEvents = 7
	// offsetsDeadline bounds the wait for the consumer to commit.
	offsetsDeadline = 90 * time.Second
)

type consumerOffsetsOutput struct {
	Result struct {
		Partitions []struct {
			Group string `json:"consumer_group"`
			Lag   int64  `json:"lag"`
		} `json:"partitions"`
		TotalLag           int64 `json:"total_lag"`
		OperatorOutboxRows int64 `json:"operator_outbox_rows"`
	} `json:"result"`
}

// TestAuditConsumerOffsetsReportsLagAndOutboxRemainders runs `ops audit
// consumer-offsets` through the audited command tree against a real Kafka
// broker, YugabyteDB ledger, and FoundationDB cluster, with the real audit
// consumer. The consumer projects every produced event and the report shows
// lag 0. Events produced after the consumer stops raise the reported lag by
// their count. Each audited run adds one operator outbox row, which the
// ledger reader counts through migration 016. Without that grant the command
// fails.
func TestAuditConsumerOffsetsReportsLagAndOutboxRemainders(t *testing.T) {
	ctx := t.Context()
	adminDSN := testenv.Ledger(t)
	t.Setenv("DATABASE_URL", adminDSN)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	brokers := testenv.Kafka(t)
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.AuditReaderDSN = readerLoginDSN(t, admin, adminDSN)
	cfg.AuditKafkaBrokers = brokers
	cfg.AuditKafkaTopic = "audit.offsets-test-" + uuid.NewString()[:8]
	group := "tack-offsets-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(ctx, audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: cfg.AuditKafkaTopic, GroupID: group,
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: adminDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(ctx)
	produceAuditEvents(t, cfg, consumedEvents)
	run := consumerOffsetsCommand(t, cfg, admin)

	first := waitForLag(t, run, 0)
	if len(first.Result.Partitions) == 0 || first.Result.Partitions[0].Group != group {
		t.Fatalf("report partitions = %+v, want rows of group %s", first.Result.Partitions, group)
	}
	if err := consumer.Close(); err != nil {
		t.Fatalf("stop the audit consumer: %v", err)
	}
	produceAuditEvents(t, cfg, waitingEvents)
	second, err := run()
	if err != nil {
		t.Fatalf("consumer-offsets after the consumer stopped: %v", err)
	}
	if second.Result.TotalLag != waitingEvents {
		t.Fatalf("total lag = %d, want %d", second.Result.TotalLag, waitingEvents)
	}
	if second.Result.OperatorOutboxRows <= first.Result.OperatorOutboxRows {
		t.Fatalf("operator outbox rows = %d after %d, want the audited run's row counted",
			second.Result.OperatorOutboxRows, first.Result.OperatorOutboxRows)
	}

	revoke := `REVOKE SELECT (event_id, created_at) ON public.ops_outbox FROM audit_reader`
	if _, err := admin.Exec(ctx, revoke); err != nil {
		t.Fatalf("revoke the migration 016 grant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `GRANT SELECT (event_id, created_at) ON public.ops_outbox TO audit_reader`)
	})
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "operator outbox remainder") {
		t.Fatalf("consumer-offsets without the grant = %v, want an operator outbox remainder error", err)
	}
}

// consumerOffsetsCommand returns a function that runs the audited command
// once with the real SQL outbox and decodes its JSON result.
func consumerOffsetsCommand(t *testing.T, cfg *config.Config, pool *pgxpool.Pool) func() (consumerOffsetsOutput, error) {
	t.Helper()
	output := &bytes.Buffer{}
	factory := cli.System(cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	return func() (consumerOffsetsOutput, error) {
		output.Reset()
		root := searchCommandRoot(factory)
		root.SetContext(t.Context())
		root.SetArgs([]string{
			"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
			"--operator-email", "operator@example.com", "--operator-name", "Offsets Test",
			"ops", "audit", "consumer-offsets",
		})
		var decoded consumerOffsetsOutput
		if err := root.Execute(); err != nil {
			return decoded, err
		}
		if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
			t.Fatalf("decode the consumer-offsets output %q: %v", output.String(), err)
		}
		return decoded, nil
	}
}

// waitForLag runs the command until the reported total lag equals want.
func waitForLag(t *testing.T, run func() (consumerOffsetsOutput, error), want int64) consumerOffsetsOutput {
	t.Helper()
	deadline := clock.Now().Add(offsetsDeadline)
	for {
		report, err := run()
		if err != nil {
			t.Fatalf("consumer-offsets: %v", err)
		}
		if report.Result.TotalLag == want {
			return report
		}
		if clock.Now().After(deadline) {
			t.Fatalf("total lag = %d after %s, want %d", report.Result.TotalLag, offsetsDeadline, want)
		}
		time.Sleep(time.Second)
	}
}
