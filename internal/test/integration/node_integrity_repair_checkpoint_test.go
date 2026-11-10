package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairCheckpointAdvancesForward checks that
// AdvanceRepairCheckpoint on the FoundationDB repair store records a first
// checkpoint at exported. The store must refuse a change that expects the
// checkpoint not to exist, a change from exported to exported, and a change
// from exported to an unknown phase with domain.ErrConflict. The stored phase
// must stay exported after those refusals. The store must then accept a change
// from exported to completed. The store must refuse a change from completed
// back to exported, and the stored phase must stay completed.
func TestNodeIntegrityRepairCheckpointAdvancesForward(t *testing.T) {
	env := integration.SetupTestEnv(t)
	repairs := env.Stores.Repairs()
	checkpoint := node.RepairCheckpoint{
		RepairID: "tack-582-checkpoint-order", ManifestDigest: strings.Repeat("a", 64), NodeID: uuid.New(),
		SourceDigest: strings.Repeat("b", 64), Phase: node.RepairPhaseExported, ActorID: uuid.New(), UpdatedAt: time.Now().UTC(),
	}
	if err := repairs.AdvanceRepairCheckpoint(env.Ctx, "", checkpoint); err != nil {
		t.Fatalf("record the first checkpoint at %s: %v", checkpoint.Phase, err)
	}
	refused := []struct {
		previous node.RepairPhase
		next     node.RepairPhase
	}{
		{previous: "", next: node.RepairPhaseCompleted},
		{previous: node.RepairPhaseExported, next: node.RepairPhaseExported},
		{previous: node.RepairPhaseExported, next: "unknown"},
	}
	for _, transition := range refused {
		requireCheckpointRefused(t, env, transition.previous, checkpoint, transition.next)
	}
	requireCheckpointPhase(t, env, checkpoint.RepairID, node.RepairPhaseExported)

	checkpoint.Phase = node.RepairPhaseCompleted
	if err := repairs.AdvanceRepairCheckpoint(env.Ctx, node.RepairPhaseExported, checkpoint); err != nil {
		t.Fatalf("advance the checkpoint from %s to %s: %v", node.RepairPhaseExported, checkpoint.Phase, err)
	}
	requireCheckpointRefused(t, env, node.RepairPhaseCompleted, checkpoint, node.RepairPhaseExported)
	requireCheckpointPhase(t, env, checkpoint.RepairID, node.RepairPhaseCompleted)
}

func requireCheckpointRefused(
	t *testing.T, env *integration.TestEnv, previous node.RepairPhase, checkpoint node.RepairCheckpoint, next node.RepairPhase,
) {
	t.Helper()
	checkpoint.Phase = next
	err := env.Stores.Repairs().AdvanceRepairCheckpoint(env.Ctx, previous, checkpoint)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("advance checkpoint %s from %q to %q = %v, want a conflict", checkpoint.RepairID, previous, next, err)
	}
}
