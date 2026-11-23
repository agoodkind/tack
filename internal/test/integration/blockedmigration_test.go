package integration_test

import (
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func TestParentRelationshipMigrationRefusesLegacyCycle(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	first := createRepairIssue(t, env, project, "First legacy cycle issue")
	second := createRepairIssue(t, env, project, "Second legacy cycle issue")
	storeLegacyParentFixture(t, env, first, second, true)
	storeLegacyParentFixture(t, env, second, first, true)
	requireParentMigrationRefused(t, []uuid.UUID{first, second}, "cycle")
}

func TestParentRelationshipMigrationRefusesViewOnlyParent(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	issue := createRepairIssue(t, env, project, "View-only legacy parent")
	storeLegacyParentFixture(t, env, issue, project, true)
	removePrimaryParentFixture(t, env, issue)
	requireParentMigrationRefused(t, []uuid.UUID{issue}, "only the read view")
}

func TestParentRelationshipMigrationRefusesMissingResolution(t *testing.T) {
	env, project := parentMigrationRefusalScope(t)
	issue := createRepairIssue(t, env, project, "Legacy parent without resolution")
	storeLegacyParentFixture(t, env, issue, project, true)
	removeParentResolutionFixture(t, issue)
	requireParentMigrationRefused(t, []uuid.UUID{issue}, "resolution")
}

func parentMigrationRefusalScope(t *testing.T) (*integration.TestEnv, uuid.UUID) {
	t.Helper()
	env := integration.SetupTestEnv(t)
	actor := uuid.Must(uuid.NewV7())
	workspace := createCommandScope(t, env, "workspace", env.OrgID, actor)
	project := createCommandScope(t, env, "project", workspace, actor)
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

	valid := createRepairIssue(t, env, project, "Valid legacy parent before refusal")
	storeLegacyParentFixture(t, env, valid, project, true)

	return env, project
}

func requireParentMigrationRefused(t *testing.T, nodeIDs []uuid.UUID, condition string) {
	t.Helper()
	before := parentMigrationRefusalRecords(t)
	pool := ledgerAdminPool(t)
	cfg := &config.Config{
		DatabaseURL:           testenv.Ledger(t),
		FDBClusterFile:        testenv.FoundationDB(t),
		FDBTransactionTimeout: nodeAuditTransactionTimeout,
	}

	for _, execute := range []bool{false, true} {
		arguments := []string{
			"--operator-id",
			uuid.Must(uuid.NewV7()).String(),
			"--operator-email",
			"parent-refusal@example.com",
			"ops",
			"backfill",
			"once-parent-relationships",
		}
		if execute {
			arguments = append([]string{"--execute"}, arguments...)
		}

		output, err := runOpsCommand(t, cfg, pool, arguments...)
		if err == nil || !strings.Contains(err.Error(), condition) {
			t.Fatalf(
				"parent migration execute=%t returned %v, want %q refusal\n%s",
				execute,
				err,
				condition,
				output,
			)
		}

		selectedNode := false

		for _, nodeID := range nodeIDs {
			selectedNode = selectedNode || strings.Contains(err.Error(), nodeID.String())
		}

		if !selectedNode {
			t.Fatalf(
				"parent migration refusal does not identify selected nodes %v: %v",
				nodeIDs,
				err,
			)
		}

		after := parentMigrationRefusalRecords(t)
		if !maps.Equal(before, after) {
			t.Fatalf(
				"parent migration execute=%t changed primary, view, index, relationship, or schema records before refusal",
				execute,
			)
		}
	}
}
