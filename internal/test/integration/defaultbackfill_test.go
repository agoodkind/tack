package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

func TestDefaultChildrenBackfillUsesRelationships(t *testing.T) {
	environment := SetupTestEnv(t)
	registerOpsOrg(t, environment)
	actor := uuid.New()
	workspace := mustCreateScope(
		t,
		environment,
		"workspace",
		"Main",
		environment.OrgID,
		environment.OrgID,
		actor,
	)
	project := mustCreateScope(t, environment, "project", "Fan", workspace.ID, workspace.ID, actor)
	query := node.NodeListQuery{
		OrgID:            environment.OrgID,
		NodeType:         "state",
		ByTargetRelation: &node.RelationScan{NodeID: project.ID, RelationType: node.RelChildOf},
	}
	children, err := environment.Stores.Views.List(environment.Ctx, query)
	if err != nil || len(children) == 0 {
		t.Fatalf("list default states: %v, %v", children, err)
	}

	originalCount := len(children)
	types, err := environment.Stores.NodeTypes.List(environment.Ctx, environment.OrgID)
	if err != nil {
		t.Fatalf("list node types: %v", err)
	}

	const missingName = "Backfilled state"

	for _, nodeType := range types {
		if nodeType.TypeKey != "project" {
			continue
		}

		nodeType.DefaultChildren = append(
			nodeType.DefaultChildren,
			node.DefaultChild{TypeKey: "state", Name: missingName},
		)
		if err := environment.Stores.NodeTypes.Set(environment.Ctx, nodeType); err != nil {
			t.Fatalf("declare missing state: %v", err)
		}
	}

	configuration := &config.Config{
		DatabaseURL:           testenv.Ledger(t),
		FDBClusterFile:        testenv.FoundationDB(t),
		FDBTransactionTimeout: 5 * time.Second,
	}

	for run := 0; run < 2; run++ {
		if err := ops.Run(environment.Ctx, configuration, "backfill.default_children"); err != nil {
			t.Fatalf("run default child backfill %d: %v", run, err)
		}

		children, err = environment.Stores.Views.List(environment.Ctx, query)
		if err != nil || len(children) != originalCount+1 {
			t.Fatalf(
				"backfill %d returned %d children and %v; expected %d",
				run,
				len(children),
				err,
				originalCount+1,
			)
		}
	}

	indexed, err := environment.Stores.Nodes.ListByProperty(
		environment.Ctx,
		environment.OrgID,
		"state",
		"parent_id",
		jsonStr(project.ID.String()),
	)
	if err != nil {
		t.Fatalf("read the legacy parent index: %v", err)
	}

	if len(indexed) != 0 {
		t.Fatalf("found %d states in the legacy parent index", len(indexed))
	}

	for _, child := range children {
		if child.Name != missingName {
			continue
		}

		current, err := environment.Stores.Nodes.Get(environment.Ctx, environment.OrgID, child.ID)
		if err != nil || current == nil {
			t.Fatalf("read backfilled state %s: %v", child.ID, err)
		}

		if _, ok := current.Props["parent_id"]; ok {
			t.Fatal("the backfill wrote parent_id to the state")
		}

		if _, ok := child.Props["parent_id"]; ok {
			t.Fatal("the backfill wrote parent_id to the view")
		}

		if string(current.Props["scope_id"]) != string(jsonStr(project.ID.String())) {
			t.Fatalf("backfilled state %s has scope %s", child.ID, current.Props["scope_id"])
		}

		return
	}

	t.Fatalf("the backfill did not create state %q", missingName)
}
