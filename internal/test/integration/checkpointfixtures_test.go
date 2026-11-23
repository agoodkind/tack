package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

func renameNode(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) {
	t.Helper()
	name := "Renamed " + nodeID.String()
	if _, err := env.NodeSvc.Update(
		env.Ctx,
		service.UpdateInput{
			NodeID:              nodeID,
			Name:                &name,
			Props:               nil,
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
			ActorID:             uuid.New(),
		},
	); err != nil {
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

func recordCheckpoint(
	t *testing.T,
	env *integration.TestEnv,
	manifest ops.NodeIntegrityManifest,
	index int,
	phase node.RepairPhase,
) {
	t.Helper()
	entry := manifest.Entries[index]
	checkpoint := node.RepairCheckpoint{
		RepairID:       entry.RepairID,
		ManifestDigest: manifest.Digest,
		NodeID:         entry.NodeID,
		SourceDigest:   entry.Source.PrimaryDigest,
		Phase:          phase,
		ActorID:        uuid.New(),
		UpdatedAt:      time.Now().UTC(),
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
