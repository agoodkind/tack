package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	changeTokenLabel  = "runtime-audit-command-change"
	commandMemberRole = 15
)

// TestRuntimeAuditCommandChanges runs operator commands that change SQL and
// FoundationDB records through the clispec command gate. Each change event
// must use the operation ID that the gate records in the command intent and
// must have the operator as its actor.
func TestRuntimeAuditCommandChanges(t *testing.T) {
	t.Run("sql_token_create", testCommandTokenChange)
	t.Run("foundationdb_default_children", testCommandNodeChange)
	t.Run("foundationdb_reference_repair", testCommandReferenceRepair)
}

func testCommandTokenChange(t *testing.T) {
	pool := ledgerAdminPool(t)
	holder := createSQLAuditUser(t, pool, sqlAuditPrincipal())
	operatorID := uuid.Must(uuid.NewV7())
	cfg := &config.Config{DatabaseURL: testenv.Ledger(t)}
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "auth", "token-create",
		"--email", holder.Email, "--label", changeTokenLabel); err != nil {
		t.Fatalf("token-create failed: %v\n%s", err, output)
	}
	commandID := commandOperationID(t, outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsAuthTokenCreate), operatorID.String()))
	changes := 0
	for _, event := range sqlOutboxEvents(t, pool, commandID) {
		if event.Context.Phase != audit.PhaseChange {
			continue
		}
		requireChangeEvent(t, event, commandID, operatorID)
		changes++
	}
	if changes != 1 {
		t.Fatalf("token-create recorded %d change events with operation %s instead of 1", changes, commandID)
	}
}

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
		DatabaseURL: testenv.Ledger(t), FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: 5 * time.Second,
	}
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "batch", "backfill.default_children"); err != nil {
		t.Fatalf("backfill.default_children failed: %v\n%s", err, output)
	}
	commandID := commandOperationID(t, outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql",
		string(audit.VerbOpsBackfillDefaultChildren), operatorID.String()))
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
		t.Fatalf("the backfill recorded %d creation events for state %s instead of 1", len(events), restored.ID)
	}
	requireChangeEvent(t, events[0], commandID, operatorID)
}

// The default children backfill reads its organizations from org_members.
func addCommandOrgMember(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	principal := sqlAuditPrincipal()
	member := createSQLAuditUser(t, pool, principal)
	ctx, _ := sqlAuditContext(t, principal)
	err := postgres.NewOrgMemberRepo(pool).AddMember(ctx, &org.Member{
		ID: uuid.Nil, OrgID: orgID, UserID: member.ID, Role: commandMemberRole, CreatedAt: time.Time{},
	})
	if err != nil {
		t.Fatalf("adding user %s to organization %s failed: %v", member.ID, orgID, err)
	}
}

func commandOperationID(t *testing.T, events []audit.Event) uuid.UUID {
	t.Helper()
	var intents []audit.Event
	for _, event := range events {
		if event.Outcome == audit.OutcomePending {
			intents = append(intents, event)
		}
	}
	if len(intents) != 1 {
		t.Fatalf("the outbox has %d command intents instead of 1", len(intents))
	}
	operationID := decodeCommandExtra(t, intents[0]).OpID
	if operationID == uuid.Nil {
		t.Fatalf("command intent %s does not record an operation ID", intents[0].EventID)
	}
	return operationID
}

func createCommandScope(t *testing.T, env *integration.TestEnv, typeKey string, parentID, actor uuid.UUID) uuid.UUID {
	t.Helper()
	props := map[string]json.RawMessage{"slug": json.RawMessage(strconv.Quote("changes"))}
	if typeKey == "project" {
		props["identifier"] = json.RawMessage(strconv.Quote("CHG"))
	}
	return createRepairNode(t, env, service.CreateInput{
		ParentID: parentID, ScopeID: parentID, NodeTypeKey: typeKey, Name: "Changes", Props: props,
		Relationships: nil, ActorID: actor, IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
}

func projectStates(t *testing.T, env *integration.TestEnv, projectID uuid.UUID) []*node.Node {
	t.Helper()
	parent := json.RawMessage(strconv.Quote(projectID.String()))
	states, err := env.Stores.Nodes.ListByProperty(env.Ctx, env.OrgID, "state", "parent_id", parent)
	if err != nil || len(states) == 0 {
		t.Fatalf("project %s has %d states (list error %v); the test requires at least one", projectID, len(states), err)
	}
	return states
}
