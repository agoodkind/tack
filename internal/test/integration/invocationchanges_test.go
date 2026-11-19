package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func testCommandNodeChange(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.New()
	workspace := createCommandScope(t, env, "workspace", env.OrgID, actor)
	project := createCommandScope(t, env, "project", workspace, actor)
	removed := projectStates(t, env, project)[0]
	if _, err := env.NodeSvc.Delete(env.Ctx, removed.ID, actor); err != nil {
		t.Fatalf("deleting state %s failed: %v", removed.ID, err)
	}

	pool := ledgerAdminPool(t)
	addCommandOrgMember(t, pool, env.OrgID)
	mark := outboxTail(t, env)
	operatorID := uuid.Must(uuid.NewV7())
	cfg := &config.Config{
		DatabaseURL:           testenv.Ledger(t),
		FDBClusterFile:        testenv.FoundationDB(t),
		FDBTransactionTimeout: 5 * time.Second,
	}
	if output, err := runOpsCommand(
		t,
		cfg,
		pool,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"batch",
		"backfill.default_children",
	); err != nil {
		t.Fatalf("backfill.default_children failed: %v\n%s", err, output)
	}

	commandID := commandOperationID(
		t,
		outboxEventsByQuery(
			t,
			pool,
			"runtime_audit_verb_outbox.sql",
			string(audit.VerbOpsBackfillDefaultChildren),
			operatorID.String(),
		),
	)
	var restored *node.Node

	for _, state := range projectStates(t, env, project) {
		if state.Name == removed.Name {
			restored = state
		}
	}

	if restored == nil || restored.ID == removed.ID {
		t.Fatalf("the backfill did not restore state %q of project %s", removed.Name, project)
	}

	events := changeEventsFor(outboxEventsAfter(t, env, mark), audit.VerbNodeCreate, restored.ID)
	if len(events) != 1 {
		t.Fatalf(
			"the backfill recorded %d creation events for state %s instead of 1",
			len(events),
			restored.ID,
		)
	}

	requireChangeEvent(t, events[0], commandID, operatorID)
}
