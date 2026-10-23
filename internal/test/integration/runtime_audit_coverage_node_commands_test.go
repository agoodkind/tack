package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// coverageLegacyProperty is the property key that repair cases add to a node
// and remove through a repair profile.
const coverageLegacyProperty = "coverage_legacy_note"

// addNodeCommandCases adds node inspection, validation, repair, creation, and
// batch command cases with their expected audit verbs and outcomes.
func addNodeCommandCases(cases map[string]coverageCase) {
	cases["command ops inspect read"] = commandCase(audit.VerbOpsInspectRead, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{"ops", "inspect", "read", "--node", env.fixture(t).nodes["issue"].String()}, configure: nil, outbox: nil}
	})
	cases["command ops inspect find"] = commandCase(audit.VerbOpsInspectFind, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{"ops", "inspect", "find", "--org", env.server.orgID.String(), "--type", "issue"}, configure: nil, outbox: nil}
	})
	cases["command ops inspect query"] = commandCase(audit.VerbOpsInspectQuery, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{
			"ops", "inspect", "query", "--org", env.server.orgID.String(), "--type", "workspace",
			"--property", "slug", "--value", strconv.Quote(env.server.workspace),
		}, configure: nil, outbox: nil}
	})
	cases["command ops verify node"] = commandCase(audit.VerbOpsVerifyNode, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{"ops", "verify", "node", "--node", env.fixture(t).nodes["issue"].String()}, configure: nil, outbox: nil}
	})
	cases["command ops repair classes"] = commandCase(audit.VerbOpsRepairClasses, audit.OutcomeOK, fixedCommand("ops", "repair", "classes"))
	cases["command ops validate node"] = commandCase(audit.VerbOpsValidateNode, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		nodeID, profile := env.legacyRepair(t)
		return commandInput{args: []string{
			"ops", "validate", "node", "--node", nodeID.String(), "--class", string(ops.RepairClassPropsTransform), "--profile", profile,
		}, configure: nil, outbox: nil}
	})
	cases["command ops repair preview"] = commandCase(audit.VerbOpsRepairPreview, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		nodeID, profile := env.legacyRepair(t)
		return commandInput{args: []string{
			"ops", "repair", "preview", "--node", nodeID.String(), "--class", string(ops.RepairClassPropsTransform), "--profile", profile,
		}, configure: nil, outbox: nil}
	})
	cases["command ops repair apply"] = commandCase(audit.VerbOpsRepairApply, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		nodeID, profile := env.legacyRepair(t)
		return commandInput{args: []string{
			"ops", "repair", "apply", "--actor", env.server.userID.String(), "--yes", "--node", nodeID.String(),
			"--class", string(ops.RepairClassPropsTransform), "--profile", profile, "--confirm", env.repairToken(t, nodeID),
		}, configure: nil, outbox: nil}
	})
	cases["command ops repair reference-uniqueness"] = commandCase(audit.VerbOpsRepairReferenceUniqueness, audit.OutcomeOK,
		fixedCommand("ops", "repair", "reference-uniqueness", "--keep", "oldest"))
	cases["command ops act-as create"] = commandCase(audit.VerbOpsActAsCreate, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{
			"ops", "act-as", "create", "--user", env.userEmail(t), "--reason", "coverage", "--parent", env.fixture(t).nodes["project"].String(),
			"--node-type", "issue", "--name", "Coverage act-as " + uuid.NewString()[:8],
		}, configure: nil, outbox: nil}
	})
	cases["command ops batch backfill.default_children"] = commandCase(audit.VerbOpsBackfillDefaultChildren, audit.OutcomeOK,
		fixedCommand("ops", "batch", "backfill.default_children"))
	cases["command ops batch reference.duplicates"] = commandCase(audit.VerbOpsReferenceDuplicates, audit.OutcomeOK,
		fixedCommand("ops", "batch", "reference.duplicates"))
	cases["command ops batch reindex"] = commandCase(audit.VerbOpsReindex, audit.OutcomeOK, fixedCommand("ops", "batch", "reindex"))
}

// stores opens FoundationDB stores using the test server's cluster file.
func (env *coverageEnv) stores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	stores, err := fdbadapter.NewStores(env.server.clusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("FoundationDB store initialization failed; the command case requires access to the test cluster: %v", err)
	}
	return stores
}

// legacyRepair creates an issue with a legacy property and writes a repair
// profile that removes that property. It returns the issue ID and profile path.
func (env *coverageEnv) legacyRepair(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	stores := env.stores(t)
	nodeID := env.fixture(t).create(t, "issue")
	stored, err := stores.Nodes.Get(ctx, env.server.orgID, nodeID)
	if err != nil || stored == nil {
		t.Fatalf("Node lookup failed or returned nil; the repair case requires the issue node %s: %v", nodeID, err)
	}
	view, err := stores.Views.Get(ctx, nodeID)
	if err != nil {
		t.Fatalf("NodeListView lookup failed; the repair case requires the read row for node %s: %v", nodeID, err)
	}
	stored.Props[coverageLegacyProperty] = json.RawMessage(strconv.Quote("legacy coverage text"))
	view.Props = stored.Props
	if err := stores.Nodes.Set(ctx, stored, view); err != nil {
		t.Fatalf("Saving the legacy property failed; the repair case requires the property on node %s: %v", nodeID, err)
	}
	profile, err := json.Marshal(env.repairProfile())
	if err != nil {
		t.Fatalf("Repair profile encoding failed; the command case requires a JSON profile: %v", err)
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, profile, 0o600); err != nil {
		t.Fatalf("Writing the repair profile failed; the command case requires a profile file: %v", err)
	}
	return nodeID, path
}

// repairProfile returns a property transformation profile that removes
// coverageLegacyProperty.
func (env *coverageEnv) repairProfile() *ops.RepairReferenceProfile {
	return &ops.RepairReferenceProfile{
		Name: "coverage", RemoveFields: []string{coverageLegacyProperty},
	}
}

// repairToken previews the property repair and returns its confirmation token.
// It fails the test if the preview fails or does not permit application.
func (env *coverageEnv) repairToken(t *testing.T, nodeID uuid.UUID) string {
	t.Helper()
	stores := env.stores(t)
	console := ops.NewRepairConsole(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships)
	preview, err := console.Preview(integration.WithTestOperation(t, t.Context()), ops.RepairPreviewInput{
		Class: ops.RepairClassPropsTransform, NodeID: nodeID, Profile: env.repairProfile(),
	})
	if err != nil || !preview.CanApply {
		t.Fatalf("Repair preview failed or refused application; the test requires an applicable repair for node %s: %+v %v", nodeID, preview, err)
	}
	return preview.ConfirmationToken
}
