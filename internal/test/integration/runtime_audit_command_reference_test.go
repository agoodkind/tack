package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// testCommandReferenceRepair stores an epic and an issue with the same
// generated sequence in one project and runs ops repair reference-uniqueness
// with --keep oldest through the command gate. The repair must record a
// node.update event for the renumbered newer node, and every change event of
// the run must use the gate's operation ID and the operator as its actor.
func testCommandReferenceRepair(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.New()
	workspace := createCommandScope(t, env, "workspace", env.OrgID, actor)
	project := createCommandScope(t, env, "project", workspace, actor)
	first := createDuplicateReference(t, env, "epic", project)
	second := createDuplicateReference(t, env, "issue", project)
	pool := ledgerAdminPool(t)
	addCommandOrgMember(t, pool, env.OrgID)
	mark := outboxTail(t, env)
	operatorID := uuid.Must(uuid.NewV7())
	cfg := &config.Config{
		DatabaseURL: testenv.Ledger(t), FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: 5 * time.Second,
	}
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "repair", "reference-uniqueness", "--keep", "oldest"); err != nil {
		t.Fatalf("reference-uniqueness failed: %v\n%s", err, output)
	}
	commandID := commandOperationID(t, outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsRepairReferenceUniqueness), operatorID.String()))
	renumbered := second
	if first.String() > second.String() {
		renumbered = first
	}
	events := outboxEventsAfter(t, env, mark)
	if len(changeEventsFor(events, audit.VerbNodeUpdate, renumbered)) == 0 {
		t.Fatalf("the repair recorded no node.update event for renumbered node %s in %+v", renumbered, events)
	}
	for _, event := range events {
		if event.Context.Phase == audit.PhaseChange {
			requireChangeEvent(t, event, commandID, operatorID)
		}
	}
}

// createDuplicateReference stores a node of typeKey under projectID with
// generated sequence 1 and without a reference claim, the stored shape of a
// node written before reference uniqueness. It returns the node ID.
func createDuplicateReference(t *testing.T, env *integration.TestEnv, typeKey string, projectID uuid.UUID) uuid.UUID {
	t.Helper()
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{
		"parent_id": json.RawMessage(strconv.Quote(projectID.String())),
		"scope_id":  json.RawMessage(strconv.Quote(projectID.String())),
		"sequence":  json.RawMessage("1"),
	}
	current := &node.Node{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, NodeType: typeKey, Name: "Duplicate " + typeKey,
		Props: props, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name,
		Props: props, CreatedAt: now, UpdatedAt: now,
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, current, view, nil,
		[]string{"parent_id", "scope_id", "sequence"}, nil, nil); err != nil {
		t.Fatalf("store the duplicate %s: %v", typeKey, err)
	}
	return current.ID
}
