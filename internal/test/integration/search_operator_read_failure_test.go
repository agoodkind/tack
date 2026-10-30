package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchOperatorReadFailureBoundaries checks real admission and finalization
// refusals, command failures, and cancellation through the migrated commands.
func TestSearchOperatorReadFailureBoundaries(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	integration.ConfigureSearchMaintenance(t, testenv.OpenSearch(t))
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	fixture.Config.AuditReaderDSN = fixture.Config.DatabaseURL
	pool := ledgerAdminPool(t)
	var terminal []audit.Event
	for _, current := range []struct {
		command   string
		verb      audit.Verb
		reference string
	}{
		{command: "settings", verb: audit.VerbOpsSearchSettings, reference: "opensearch/settings"},
		{command: "verify", verb: audit.VerbOpsSearchVerify, reference: "opensearch/verification"},
		{command: "dlq", verb: audit.VerbOpsAuditDLQInspect, reference: "audit/dlq/summary"},
	} {
		arguments := []string{"ops", "search", current.command}
		if current.command == "dlq" {
			arguments = []string{"ops", "audit", "dlq", "inspect"}
		}
		t.Run(current.command+"_admission", func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), readTestSQL(t, "search_read_deny_admission.sql")); err != nil {
				t.Fatalf("deny the operator read admission: %v", err)
			}
			t.Cleanup(func() {
				if _, err := pool.Exec(context.WithoutCancel(t.Context()), readTestSQL(t, "search_read_allow_admission.sql")); err != nil {
					t.Errorf("restore the operator read admission: %v", err)
				}
			})
			operatorID := uuid.Must(uuid.NewV7())
			denied := rolePool(t, testenv.Ledger(t), "audit_operator")
			root, output := searchReadCommand(t, fixture.Config, denied, operatorID, arguments...)
			err := root.ExecuteContext(t.Context())
			if !errors.Is(err, audit.ErrReadNotAdmitted) || !strings.Contains(err.Error(), "ops_read_begin") || output.Len() != 0 {
				t.Fatalf("admission refusal = %v, output = %q; want a refused admission before command output", err, output.String())
			}
			if events := readOutboxEvents(t, pool, current.verb, cli.ServiceActorID(searchReadService(operatorID))); len(events) != 0 {
				t.Fatalf("refused admission recorded a legacy successful read: %+v", events)
			}
		})
		t.Run(current.command+"_finalization", func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), readTestSQL(t, "runtime_audit_public_finalize_denied.sql")); err != nil {
				t.Fatalf("configure the existing finalization-denied test role: %v", err)
			}
			operatorID := uuid.Must(uuid.NewV7())
			denied := rolePool(t, testenv.Ledger(t), finalizeDeniedRole)
			root, output := searchReadCommand(t, fixture.Config, denied, operatorID, arguments...)
			err := root.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), "finalize audit read") || output.Len() != 0 {
				t.Fatalf("finalization refusal = %v, output = %q; want withheld result bytes", err, output.String())
			}
			actorID := cli.ServiceActorID(searchReadService(operatorID))
			if events := readOutboxEvents(t, pool, current.verb, actorID); len(events) != 0 {
				t.Fatalf("failed finalization recorded a successful read: %+v", events)
			}
			recoverSearchRead(t, pool, actorID)
			event := requireSearchReadEvent(t, pool, operatorID, current.verb, current.reference)
			if event.Outcome != audit.OutcomeIncomplete || event.Error == nil || event.Context.Read != nil {
				t.Fatalf("recovered recording failure = %+v, want an incomplete read", event)
			}
			terminal = append(terminal, event)
		})
		t.Run(current.command+"_failure", func(t *testing.T) {
			cfg := *fixture.Config
			cfg.SearchEndpoint = ""
			if current.command == "dlq" {
				cfg.AuditReaderDSN = ""
			}
			operatorID := uuid.Must(uuid.NewV7())
			root, output := searchReadCommand(t, &cfg, pool, operatorID, arguments...)
			err := root.ExecuteContext(t.Context())
			if err == nil || output.Len() != 0 {
				t.Fatalf("failed command = %v, output = %q; want the functional error without result bytes", err, output.String())
			}
			event := requireSearchReadEvent(t, pool, operatorID, current.verb, current.reference)
			requireFailedSearchRead(t, event, err)
			terminal = append(terminal, event)
		})
		t.Run(current.command+"_cancellation", func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			observed := cancellationReadPool(t, cancel)
			operatorID := uuid.Must(uuid.NewV7())
			root, output := searchReadCommand(t, fixture.Config, observed, operatorID, arguments...)
			err := root.ExecuteContext(ctx)
			if !errors.Is(err, context.Canceled) || output.Len() != 0 {
				t.Fatalf("canceled command = %v, output = %q; want context cancellation without result bytes", err, output.String())
			}
			event := requireSearchReadEvent(t, pool, operatorID, current.verb, current.reference)
			requireFailedSearchRead(t, event, err)
			terminal = append(terminal, event)
		})
	}
	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	pipeline.startRelay(t, nil)
	for _, event := range terminal {
		row := pipeline.stack().waitForRow(t, event.EventID)
		if row.Outcome != event.Outcome || row.Context.Read != nil || row.Error == nil || row.Error.Message != event.Error.Message {
			t.Fatalf("ledger failure %s differs from the actual command result: %+v", event.EventID, row)
		}
		t.Logf("ledger event=%s operation=%s verb=%s outcome=%s error=%s",
			row.EventID, row.Context.OperationID, row.Action, row.Outcome, row.Error.Code)
	}
}

func requireFailedSearchRead(t *testing.T, event audit.Event, commandError error) {
	t.Helper()
	if event.Outcome != audit.OutcomeFailed || event.Error == nil || event.Error.Code != "read_failed" ||
		!strings.Contains(commandError.Error(), event.Error.Message) || event.Context.Read != nil {
		t.Fatalf("failed read = %+v, want the actual command error %v", event, commandError)
	}
}

func recoverSearchRead(t *testing.T, pool *pgxpool.Pool, actorID uuid.UUID) {
	t.Helper()
	rows, err := pool.Query(t.Context(), readTestSQL(t, "search_read_expire_actor.sql"), actorID.String())
	if err != nil {
		t.Fatalf("expire the read after finalization refusal: %v", err)
	}
	identifiers, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(identifiers) != 1 {
		t.Fatalf("pending read identities = %v, error = %v; want one admitted read", identifiers, err)
	}
	lifecycle := audit.NewPoolReadLifecycle(pool)
	count, err := lifecycle.RecoverExpired(t.Context(), audit.ReadBucket(identifiers[0]), audit.ReadRecoveryScanLimit)
	if err != nil || count < 1 {
		t.Fatalf("recover the unfinished read: count = %d, error = %v", count, err)
	}
}

type readAdmissionQuery struct{}

// admissionCancellation observes the real driver's successful admission query.
type admissionCancellation struct {
	cancel context.CancelFunc
}

func (observer admissionCancellation) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, readAdmissionQuery{}, strings.Contains(data.SQL, "public.ops_read_begin"))
}

func (observer admissionCancellation) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	admission, _ := ctx.Value(readAdmissionQuery{}).(bool)
	if admission && data.Err == nil {
		observer.cancel()
	}
}

func cancellationReadPool(t *testing.T, cancel context.CancelFunc) *pgxpool.Pool {
	t.Helper()
	settings, err := pgxpool.ParseConfig(testenv.Ledger(t))
	if err != nil {
		t.Fatalf("parse the test ledger address: %v", err)
	}
	settings.ConnConfig.Tracer = admissionCancellation{cancel: cancel}
	pool, err := pgxpool.NewWithConfig(t.Context(), settings)
	if err != nil {
		t.Fatalf("open the observed read pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
