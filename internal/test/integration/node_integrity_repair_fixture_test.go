package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// repairGraph records IDs for a project, epic, and issue. The issue's
// parent_id identifies the project. Its child_of edges target both the
// project and the epic.
type repairGraph struct {
	projectID uuid.UUID
	epicID    uuid.UUID
	issueID   uuid.UUID
}

func newRepairGraph(t *testing.T, env *integration.TestEnv) repairGraph {
	t.Helper()
	workspace := createRepairNode(t, env, service.CreateInput{
		ParentID: env.OrgID, ScopeID: env.OrgID, NodeTypeKey: "workspace", Name: "Repair",
		Props: map[string]json.RawMessage{"slug": json.RawMessage(strconv.Quote("repair"))}, ActorID: uuid.New(),
	})
	project := createRepairNode(t, env, service.CreateInput{
		ParentID: workspace, ScopeID: workspace, NodeTypeKey: "project", Name: "Repair",
		Props: map[string]json.RawMessage{
			"slug": json.RawMessage(strconv.Quote("repair")), "identifier": json.RawMessage(strconv.Quote("REPAIR")),
		},
		ActorID: uuid.New(),
	})
	epic := createRepairNode(t, env, service.CreateInput{
		ParentID: project, ScopeID: project, NodeTypeKey: "epic", Name: "Grouping epic", ActorID: uuid.New(),
	})
	issue := createRepairIssue(t, env, project, "Issue with two parent edges")
	if err := env.Stores.Relationships.Add(env.Ctx, &node.Relationship{
		OrgID: env.OrgID, SourceID: issue, RelationType: node.RelChildOf, TargetID: epic,
		CreatedBy: uuid.New(), CreatedAt: time.Now().UTC(), Props: nil,
	}); err != nil {
		t.Fatalf("add the legacy child_of edge to the epic: %v", err)
	}
	return repairGraph{projectID: project, epicID: epic, issueID: issue}
}

// selectEpicEntry returns a repair that sets the issue's parent to the epic
// and removes its redundant child_of edge to the project.
func (graph repairGraph) selectEpicEntry(repairID string) ops.NodeIntegrityEntry {
	return ops.NodeIntegrityEntry{
		RepairID: repairID, NodeID: graph.issueID, NodeType: "issue", Status: ops.RepairEntryExecutable,
		Classes: []string{"multiple_child_of_edges"},
		Evidence: []ops.RepairEvidence{{
			Kind: ops.RepairEvidenceInferred, Reference: "TACK-582 parent mappings",
			Summary: "The epic is the most specific linked container.",
		}},
		Result: ops.RepairResult{
			Props:       map[string]json.RawMessage{"parent_id": json.RawMessage(strconv.Quote(graph.epicID.String()))},
			RemoveEdges: []ops.RepairEdge{{RelationType: node.RelChildOf, TargetID: graph.projectID}},
		},
	}
}

// repairRequest creates a version 1 repair request containing entries.
func repairRequest(entries ...ops.NodeIntegrityEntry) ops.NodeIntegrityRequest {
	return ops.NodeIntegrityRequest{Version: 1, Ticket: "TACK-582", Entries: entries}
}

func createRepairNode(t *testing.T, env *integration.TestEnv, in service.CreateInput) uuid.UUID {
	t.Helper()
	created, err := env.NodeSvc.Create(env.Ctx, in)
	if err != nil || created == nil || created.View == nil {
		t.Fatalf("create %s %q: %v", in.NodeTypeKey, in.Name, err)
	}
	return created.View.ID
}

func createRepairIssue(t *testing.T, env *integration.TestEnv, projectID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	return createRepairNode(t, env, service.CreateInput{
		ParentID: projectID, ScopeID: projectID, NodeTypeKey: "issue", Name: name, ActorID: uuid.New(),
	})
}

func renameNode(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	name := "Renamed " + nodeID.String()
	if _, err := env.NodeSvc.Update(env.Ctx, service.UpdateInput{
		NodeID: nodeID, Name: &name, Props: nil, RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.New(),
	}); err != nil {
		t.Fatalf("rename node %s: %v", nodeID, err)
	}
}

func changePropertyDefinition(t *testing.T, env *integration.TestEnv) {
	t.Helper()
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil || len(definitions) == 0 {
		t.Fatalf("list property definitions: %v", err)
	}
	changed := *definitions[0]
	changed.Required = !changed.Required
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &changed); err != nil {
		t.Fatalf("change property definition %s: %v", changed.ID, err)
	}
}

func recordCheckpoint(t *testing.T, env *integration.TestEnv, manifest ops.NodeIntegrityManifest, index int, phase node.RepairPhase) {
	t.Helper()
	entry := manifest.Entries[index]
	checkpoint := node.RepairCheckpoint{
		RepairID: entry.RepairID, ManifestDigest: manifest.Digest, NodeID: entry.NodeID,
		SourceDigest: entry.Source.PrimaryDigest, Phase: phase, ActorID: uuid.New(), UpdatedAt: time.Now().UTC(),
	}
	if err := env.Stores.Repairs().AdvanceRepairCheckpoint(env.Ctx, "", checkpoint); err != nil {
		t.Fatalf("record checkpoint %s at %s: %v", entry.RepairID, phase, err)
	}
}

func requireRepairNodePresent(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read node %s: %v, want the node", nodeID, err)
	}
}
