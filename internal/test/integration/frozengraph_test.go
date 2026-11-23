package integration

import (
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func TestRepairEvidenceUsesRelationshipsWithoutChangingPrimary(t *testing.T) {
	environment := SetupTestEnv(t)
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
	project := mustCreateScope(
		t,
		environment,
		"project",
		"Proof",
		workspace.ID,
		workspace.ID,
		actor,
	)
	issueID := createTestIssue(t, environment, project.ID, "Relationship evidence")
	current, err := environment.Stores.Nodes.Get(environment.Ctx, environment.OrgID, issueID)
	if err != nil || current == nil {
		t.Fatalf("read issue %s: %v", issueID, err)
	}

	view, err := environment.Stores.Views.Get(environment.Ctx, issueID)
	if err != nil || view == nil {
		t.Fatalf("read issue view %s: %v", issueID, err)
	}

	previous := maps.Clone(current.Props)
	wrongParent := uuid.New()
	current.Props["parent_id"] = jsonStr(wrongParent.String())
	view.Props = maps.Clone(current.Props)
	if err := environment.Stores.Nodes.UpdateAtomic(
		environment.Ctx,
		current,
		view,
		previous,
		[]string{"parent_id"},
		nil,
	); err != nil {
		t.Fatalf("write inconsistent parent property: %v", err)
	}

	request := fdbadapter.RepairEvidenceRequest{NodeID: issueID}
	evidence, err := environment.Stores.Repairs().RepairEvidence(environment.Ctx, request)
	if err != nil || evidence.Node.ParentID != project.ID || len(evidence.Chain) == 0 ||
		evidence.Chain[0] != project.ID {
		t.Fatalf("relationship evidence = %+v, %v; expected project %s", evidence, err, project.ID)
	}

	var primary node.Node
	if err := json.Unmarshal(evidence.Node.Primary, &primary); err != nil {
		t.Fatalf("decode frozen primary: %v", err)
	}

	if string(primary.Props["parent_id"]) != string(jsonStr(wrongParent.String())) {
		t.Fatal("reading evidence changed the frozen parent property")
	}

	if err := environment.Stores.Relationships.Add(
		environment.Ctx,
		&node.Relationship{
			OrgID:        environment.OrgID,
			SourceID:     issueID,
			RelationType: node.RelChildOf,
			TargetID:     workspace.ID,
			CreatedBy:    actor,
			CreatedAt:    time.Now().UTC(),
		},
	); err != nil {
		t.Fatalf("add ambiguous parent: %v", err)
	}

	evidence, err = environment.Stores.Repairs().RepairEvidence(environment.Ctx, request)
	if err != nil || evidence.Node.ParentID != uuid.Nil || len(evidence.Outgoing) != 2 {
		t.Fatalf(
			"ambiguous evidence = %+v, %v; expected both parent edges without a selected parent",
			evidence,
			err,
		)
	}
}
