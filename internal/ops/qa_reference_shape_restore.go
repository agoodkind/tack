package ops

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

// referenceShapeSequenceProp is the property whose value decides the
// reference an issue renders, and so the one a repair changes when it
// renames a collision.
const referenceShapeSequenceProp = "sequence"

// restoreReferenceShapeNode puts an existing node back on the shape when a
// repair has moved it off. It returns 1 when it rewrote the node and 0 when
// the node already matched.
//
// Without this a re-run wrote nothing over a repaired corpus: every node
// already existed, so the generator reported the collisions its shape
// describes while the org held none, and the repair it exists to exercise had
// nothing to do (TACK-475). Restoring the sequence recreates the collision the
// duplicate scan renders from props, and releasing the node's reference claims
// hands back the unique reference the earlier repair gave it, so the next
// repair can allocate afresh rather than refuse on a claim the node no longer
// renders.
func restoreReferenceShapeNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	existing *node.NodeView,
	input referenceShapeNode,
) (int, error) {
	wanted, shapesSequence := input.Props[referenceShapeSequenceProp]
	if !shapesSequence || bytes.Equal(existing.Props[referenceShapeSequenceProp], wanted) {
		return 0, nil
	}
	current, err := stores.Nodes.Get(ctx, input.OrgID, input.ID)
	if err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.restore_read_failed",
			slog.String("node_id", input.ID.String()), slog.String("err", err.Error()))
		return 0, fmt.Errorf("read %s %s to restore the reference shape: %w", input.TypeKey, input.ID, err)
	}
	if current == nil {
		return 0, fmt.Errorf("%s %s has a view but no node; the reference shape cannot restore it",
			input.TypeKey, input.ID)
	}
	restored := *current
	restored.Name = input.Name
	restored.Props = input.Props
	restored.UpdatedBy = uuid.Nil
	restored.UpdatedAt = referenceShapeEpoch
	view := &node.NodeView{
		ID: restored.ID, OrgID: restored.OrgID, NodeType: restored.NodeType, Name: restored.Name,
		Props: restored.Props, CreatedBy: restored.CreatedBy, UpdatedBy: restored.UpdatedBy,
		CreatedAt: restored.CreatedAt, UpdatedAt: restored.UpdatedAt,
	}
	if err := stores.Nodes.UpdateAtomic(
		ctx, &restored, view, current.Props, input.Indexed, []node.ReferenceKey{},
	); err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.restore_failed",
			slog.String("node_id", input.ID.String()), slog.String("err", err.Error()))
		return 0, fmt.Errorf("restore %s %s to the reference shape: %w", input.TypeKey, input.ID, err)
	}
	return 1, nil
}

// referenceShapeLive is what the org holds after writing, counted through the
// same scan the repair uses to find its work.
type referenceShapeLive struct {
	// Collisions is the number of references held by more than one node.
	Collisions int
	// Renames is the number of nodes a repair would move: every holder of a
	// collided reference but the one it keeps.
	Renames int
}

// countLiveReferenceCollisions reports the collisions the org holds now and
// the renames they imply. The report carries both beside what the shape
// describes, so a run that collided nothing says so, and a corpus with the
// right number of groups but a member missing from one of them is caught by
// the rename count rather than passing on the group count alone.
func countLiveReferenceCollisions(ctx context.Context, env *Env, orgID uuid.UUID) (referenceShapeLive, error) {
	duplicates, err := findOrgDuplicateReferences(ctx, env, orgID)
	if err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.live_collision_count_failed",
			slog.String("org_id", orgID.String()), slog.String("err", err.Error()))
		return referenceShapeLive{}, fmt.Errorf("count live collisions in the reference shape org: %w", err)
	}
	live := referenceShapeLive{Collisions: len(duplicates), Renames: 0}
	for _, duplicate := range duplicates {
		live.Renames += len(duplicate.NodeIDs) - 1
	}
	return live, nil
}
