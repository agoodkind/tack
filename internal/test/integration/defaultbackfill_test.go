package integration

import (
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

func TestDefaultChildrenBackfillUsesRelationshipsAndKeepsParentWrites(t *testing.T) {
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
	existing := children[0]
	current, err := environment.Stores.Nodes.Get(environment.Ctx, environment.OrgID, existing.ID)
	if err != nil || current == nil {
		t.Fatalf("read default state %s: %v", existing.ID, err)
	}

	previous := maps.Clone(current.Props)
	delete(current.Props, "parent_id")
	existing.Props = maps.Clone(current.Props)
	if err := environment.Stores.Nodes.UpdateAtomic(
		environment.Ctx,
		current,
		existing,
		previous,
		[]string{"parent_id"},
		nil,
	); err != nil {
		t.Fatalf("remove the existing state's parent property: %v", err)
	}

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

	for _, child := range indexed {
		if child.Name == missingName &&
			string(child.Props["parent_id"]) == string(jsonStr(project.ID.String())) {
			return
		}
	}

	t.Fatal("the new default child does not have its parent property and index")
}
