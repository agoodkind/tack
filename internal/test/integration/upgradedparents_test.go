package integration_test

import (
	"encoding/json"
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// TestParentRelationshipMigration runs the public backfill command against
// legacy primary records, read views, indexes, and metadata. Preview must not
// change them. Execution must repair missing edges, preserve existing edges,
// remove the property and its definition, and record committed old/new values
// under the operator identity. Repeating execution must not add change events.
func TestParentRelationshipMigration(t *testing.T) {
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	workspace := createCommandScope(t, env, "workspace", env.OrgID, actor)
	project := createCommandScope(t, env, "project", workspace, actor)
	missing := createRepairIssue(t, env, project, "Missing parent relationship")
	existing := createRepairIssue(t, env, project, "Existing parent relationship")
	definition := &node.PropertyDef{
		ID:      node.SystemPropID(env.OrgID, "parent_id"),
		OrgID:   env.OrgID,
		Name:    "parent_id",
		Type:    node.PropertyTypeText,
		Indexed: true,
		Search:  &node.SearchProjection{Include: false},
	}
	if err := env.Stores.PropertyDefs.Set(env.Ctx, definition); err != nil {
		t.Fatalf("store legacy parent definition: %v", err)
	}

	storeLegacyParentFixture(t, env, missing, project, true)
	storeLegacyParentFixture(t, env, existing, workspace, false)
	staleParent := uuid.Must(uuid.NewV7())
	storeStaleParentIndexFixture(t, env, existing, staleParent)
	requireParentMigrationIndexes(
		t,
		env,
		project,
		[]uuid.UUID{missing, existing},
		false,
		staleParent,
	)
	ledger := startNodeAuditLedger(t)
	operatorID := uuid.Must(uuid.NewV7())
	arguments := []string{
		"--output",
		"json",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		"parent-migration@example.com",
		"ops",
		"backfill",
		"once-parent-relationships",
	}
	if output, err := runOpsCommand(t, ledger.cfg, ledger.admin, arguments...); err != nil {
		t.Fatalf("preview parent migration: %v\n%s", err, output)
	}

	before, err := env.Stores.Views.Get(env.Ctx, missing)
	if err != nil || before == nil ||
		string(before.Props["parent_id"]) != string(quoted(project.String())) {
		t.Fatalf("preview changed legacy parent: %+v, %v", before, err)
	}

	parents, err := env.Stores.Relationships.ListBySource(
		env.Ctx,
		env.OrgID,
		missing,
		node.RelChildOf,
	)
	if err != nil || len(parents) != 0 {
		t.Fatalf("preview changed missing relationship: %+v, %v", parents, err)
	}

	execute := append([]string{"--execute"}, arguments...)
	if output, err := runOpsCommand(t, ledger.cfg, ledger.admin, execute...); err != nil {
		t.Fatalf("execute parent migration: %v\n%s", err, output)
	}

	requireMigratedParent(t, env, missing, project)
	requireMigratedParent(t, env, existing, project)
	requireParentMigrationIndexes(
		t,
		env,
		project,
		[]uuid.UUID{missing, existing},
		true,
		workspace,
		staleParent,
	)
	removed, err := env.Stores.PropertyDefs.Get(env.Ctx, env.OrgID, definition.ID)
	if err != nil || removed != nil {
		t.Fatalf("legacy parent definition remains: %+v, %v", removed, err)
	}

	originalEvents := requireParentMigrationAudit(
		t,
		ledger,
		env,
		operatorID,
		missing,
		existing,
		project,
		definition,
	)

	repeated, err := runOpsCommand(t, ledger.cfg, ledger.admin, execute...)
	if err != nil {
		t.Fatalf("repeat parent migration: %v\n%s", err, repeated)
	}

	var repeatedOutput struct {
		Result ops.ParentRelationshipBackfillResult `json:"result"`
	}
	if err := json.Unmarshal(printedPayload(t, []byte(repeated)), &repeatedOutput); err != nil {
		t.Fatalf("decode repeated migration result: %v", err)
	}

	if repeatedOutput.Result.Changed != 0 || repeatedOutput.Result.RelationshipsAdded != 0 ||
		repeatedOutput.Result.DefinitionsRemoved != 0 {
		t.Fatalf("repeated migration reported changes: %+v", repeatedOutput.Result)
	}

	repeatedEvents := requireParentMigrationAudit(
		t,
		ledger,
		env,
		operatorID,
		missing,
		existing,
		project,
		definition,
	)
	if !maps.Equal(originalEvents, repeatedEvents) {
		t.Fatalf(
			"repeated migration changed audit event identities: %v, want %v",
			repeatedEvents,
			originalEvents,
		)
	}

	requireMigratedParent(t, env, missing, project)
	requireMigratedParent(t, env, existing, project)
	requireParentMigrationIndexes(
		t,
		env,
		project,
		[]uuid.UUID{missing, existing},
		true,
		workspace,
		staleParent,
	)
	requireRetiredParentRejected(t, env, existing, project, actor)
	ledger.requireChainVerifies(t, env.OrgID)
}

func requireRetiredParentRejected(
	t *testing.T,
	env *integration.TestEnv,
	nodeID uuid.UUID,
	parentID uuid.UUID,
	actor uuid.UUID,
) {
	t.Helper()

	for _, value := range []json.RawMessage{quoted(parentID.String()), json.RawMessage("null")} {
		_, err := env.NodeSvc.Update(
			env.Ctx,
			service.UpdateInput{
				NodeID:  nodeID,
				Props:   map[string]json.RawMessage{"parent_id": value},
				ActorID: actor,
			},
		)
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("parent_id value %s returned %v, want unknown-property rejection", value, err)
		}
	}

	requireMigratedParent(t, env, nodeID, parentID)
}
