package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/telemetry"
)

// referenceShapeDeletions is what the org's ledger records as deleted after the
// repair: the nodes named by their delete rows, and the number of delete rows
// that name no node. The reconstruction counts both on top of what is present,
// so a corpus that holds every node the repair keyed derives more than the
// repair recorded on any org that has been deleted from since (TACK-473).
type referenceShapeDeletions struct {
	Recorded   []uuid.UUID
	Unrecorded int
}

func (d referenceShapeDeletions) total() int {
	return len(d.Recorded) + d.Unrecorded
}

// readReferenceShapeDeletions reads the post-repair deletions the way the
// reconstruction does, so the corpus and the count it must produce describe
// one ledger. An org with no node types has no delete rows to read.
func readReferenceShapeDeletions(
	ctx context.Context,
	env *Env,
	querier auditRowQuerier,
	orgID uuid.UUID,
	now time.Time,
) (referenceShapeDeletions, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for the reference shape org %s: %w", orgID, err)
		telemetry.L(ctx).Error("qa.reference_shape.types_failed", slog.String("err", wrapped.Error()))
		return referenceShapeDeletions{}, wrapped
	}
	subjects, err := deletedReferenceSubjects(ctx, querier, nodeTypes, orgID, now, referenceRepairStart)
	if err != nil {
		return referenceShapeDeletions{}, err
	}
	deletions := referenceShapeDeletions{Recorded: nil, Unrecorded: 0}
	for _, subject := range subjects {
		if subject.NodeID == uuid.Nil {
			deletions.Unrecorded++
			continue
		}
		deletions.Recorded = append(deletions.Recorded, subject.NodeID)
	}
	return deletions, nil
}

// applyReferenceShapeDeletions leaves the deleted nodes out of the shape and
// returns the issues it left out, so the writer can remove any an earlier run
// created. A recorded deletion names its node, which must be one the shape
// holds: a deleted node the repair never keyed would push the reconstruction
// past the recorded count, and the corpus cannot represent that. An unrecorded
// deletion could have been any node, so the generator picks plain issues in a
// fixed order, never a collision holder or the issue that seeds a scope's
// counter, and every run leaves out the same ones.
func applyReferenceShapeDeletions(
	shape referenceShape,
	deletions referenceShapeDeletions,
) (referenceShape, []referenceShapeIssue, error) {
	absent := make(map[uuid.UUID]bool, deletions.total())
	held := make(map[uuid.UUID]bool, len(shape.Issues))
	for _, issue := range shape.Issues {
		held[issue.ID] = true
	}
	for _, nodeID := range deletions.Recorded {
		if !held[nodeID] {
			return referenceShape{}, nil, fmt.Errorf(
				"the ledger records the deletion of node %s, which the reference shape never held", nodeID)
		}
		absent[nodeID] = true
	}
	candidates := referenceShapeDeletionCandidates(shape, absent)
	if len(candidates) < deletions.Unrecorded {
		return referenceShape{}, nil, fmt.Errorf(
			"the ledger records %d deletions naming no node and the shape has only %d plain issues to leave out",
			deletions.Unrecorded, len(candidates))
	}
	for _, issue := range candidates[:deletions.Unrecorded] {
		absent[issue.ID] = true
	}
	reduced := shape
	reduced.Issues = make([]referenceShapeIssue, 0, len(shape.Issues)-len(absent))
	removed := make([]referenceShapeIssue, 0, len(absent))
	for _, issue := range shape.Issues {
		if absent[issue.ID] {
			removed = append(removed, issue)
			continue
		}
		reduced.Issues = append(reduced.Issues, issue)
	}
	reduced.Groups, reduced.Renames = referenceShapeGroupsWithout(shape.Groups, absent)
	return reduced, removed, nil
}

// referenceShapeDeletionCandidates lists the plain issues an unrecorded
// deletion may stand for, ordered by scope and sequence so the choice is the
// same on every run.
func referenceShapeDeletionCandidates(
	shape referenceShape,
	absent map[uuid.UUID]bool,
) []referenceShapeIssue {
	highWater := make(map[string]int, len(shape.Projects))
	for _, project := range shape.Projects {
		highWater[project.Identifier] = project.HighWater
	}
	colliding := make(map[string]bool, len(shape.Groups))
	for _, group := range shape.Groups {
		colliding[group.Reference] = true
	}
	candidates := make([]referenceShapeIssue, 0, len(shape.Issues))
	for _, issue := range shape.Issues {
		reference := referenceShapeReference(issue.Project, issue.Sequence)
		if absent[issue.ID] || issue.Colliding || colliding[reference] {
			continue
		}
		if issue.Sequence == highWater[issue.Project] {
			continue
		}
		candidates = append(candidates, issue)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Project != candidates[j].Project {
			return candidates[i].Project < candidates[j].Project
		}
		return candidates[i].Sequence < candidates[j].Sequence
	})
	return candidates
}

// referenceShapeGroupsWithout drops the absent nodes from every collision. A
// collision whose renamed holders are all absent is no collision at all: its
// keeper holds the reference alone, so the repair has nothing to rename there.
func referenceShapeGroupsWithout(
	groups []referenceShapeGroup,
	absent map[uuid.UUID]bool,
) ([]referenceShapeGroup, int) {
	kept := make([]referenceShapeGroup, 0, len(groups))
	renames := 0
	for _, group := range groups {
		renamed := make([]uuid.UUID, 0, len(group.Renamed))
		for _, nodeID := range group.Renamed {
			if !absent[nodeID] {
				renamed = append(renamed, nodeID)
			}
		}
		if len(renamed) == 0 {
			continue
		}
		group.Renamed = renamed
		kept = append(kept, group)
		renames += len(renamed)
	}
	return kept, renames
}

// removeReferenceShapeIssues deletes from the store any issue the ledger
// already records as deleted. An earlier generator run resurrected these
// nodes without a creation row, so the store disagreed with the ledger; the
// deletion below writes no new row because the ledger's row is the one being
// honored. It returns how many nodes it removed.
func removeReferenceShapeIssues(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	issues []referenceShapeIssue,
) (int, error) {
	removed := 0
	for _, issue := range issues {
		existing, err := env.Stores.Views.Get(ctx, issue.ID)
		if err != nil {
			wrapped := fmt.Errorf("read issue %s before honoring its deletion: %w", issue.ID, err)
			telemetry.L(ctx).Error("qa.reference_shape.deleted_read_failed", slog.String("err", wrapped.Error()))
			return removed, wrapped
		}
		if existing == nil {
			continue
		}
		if err := env.Stores.NodeDeleter.DeleteNode(ctx, orgID, issue.ID); err != nil {
			wrapped := fmt.Errorf("remove issue %s the ledger records as deleted: %w", issue.ID, err)
			telemetry.L(ctx).Error("qa.reference_shape.deleted_remove_failed", slog.String("err", wrapped.Error()))
			return removed, wrapped
		}
		removed++
	}
	return removed, nil
}
