package ops_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// probeFailureText is the error text that the provision gate returns when the
// audit infrastructure probe fails.
const probeFailureText = "probe the audit infrastructure"

// TestProvisionReturnsTheProbeFailure runs ops provision --execute with an
// operator outbox and an infrastructure probe that both connect to a port with
// no listener. The command must refuse to run and return both the probe
// connection failure and the failed intent record.
func TestProvisionReturnsTheProbeFailure(t *testing.T) {
	outboxPool, err := pgxpool.New(t.Context(), closedOutboxDSN)
	if err != nil {
		t.Fatalf("open the operator outbox pool: %v", err)
	}
	t.Cleanup(outboxPool.Close)
	factory := &cli.Factory{Cfg: nil, In: nil, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	root := executeGateRoot(t, factory)
	factory.SetOperatorIdentitySource(cli.FlagOperatorSource{Factory: factory})
	factory.SetAuditOutbox(audit.NewPoolOutbox(outboxPool))
	factory.SetAuditInfrastructureProbe(func(ctx context.Context) (audit.InfrastructureState, error) {
		connection, err := pgx.Connect(ctx, closedOutboxDSN)
		if err != nil {
			return audit.InfrastructureState{}, fmt.Errorf("connect the audit infrastructure probe: %w", err)
		}
		defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
		return audit.InfrastructureState{OutboxTableExists: false, OperatorLoginExists: false, OperatorReadsExist: false}, nil
	})
	root.SetArgs([]string{
		"ops", "provision", "--execute", "--operator-id", gateOperatorID,
		"--operator-email", "gate@example.test", "--operator-name", "Gate Test",
	})
	runErr := root.Execute()
	if runErr == nil {
		t.Fatal("ops provision ran although the probe could not connect")
	}
	message := runErr.Error()
	for _, want := range []string{probeFailureText, "connect the audit infrastructure probe", intentRecordError} {
		if !strings.Contains(message, want) {
			t.Fatalf("ops provision returned %q, want it to contain %q", message, want)
		}
	}
}
