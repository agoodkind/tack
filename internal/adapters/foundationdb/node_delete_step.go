package foundationdb

import (
	"context"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// DeleteSubtreeStep runs one step of a subtree delete job in one
// transaction. The step examines the last node on the job's stack. When a
// hierarchy child of that node exists, the step adds the child to the stack.
// When the node has more edges than one step reads, the step clears one page
// of them. Otherwise the step deletes the node, writes its ledger event, and
// removes it from the stack. The step that empties the stack writes the
// finish time into the job record, and status reads return that record. A
// finished job and a job record that no longer exists report Done. Each step
// writes one change event for each record that it changes, with the actor
// and operation ID that the job record stores. Each event ID derives from the
// operation ID, the job ID, and the record. A retried or resumed step writes
// the same event IDs.
func (s *NodeDeleteStore) DeleteSubtreeStep(ctx context.Context, jobID uuid.UUID) (progress node.SubtreeDeleteProgress, err error) {
	defer telemetry.FDBOp(ctx, "store.node.subtree_delete_step")(&err)
	err = runNodeMutation(ctx, s.nodes.db, "subtree delete step of job "+jobID.String(), func(tr fdb.Transaction) error {
		progress = node.SubtreeDeleteProgress{Deleted: 0, Moved: 0, Done: true}
		job, err := readDeleteJob(ctx, tr, jobID)
		if err != nil || job == nil {
			return err
		}
		if job.Finished() {
			progress = node.SubtreeDeleteProgress{Deleted: job.Deleted, Moved: job.Moved, Done: true}
			return nil
		}
		operation, err := jobOperation(ctx, job)
		if err != nil {
			return err
		}
		changes := jobMutationAudit(operation, job.OrgID, job.ID)
		if len(job.Stack) > 0 {
			if err := s.advance(ctx, tr, job, changes); err != nil {
				return err
			}
		}
		progress = node.SubtreeDeleteProgress{Deleted: job.Deleted, Moved: job.Moved, Done: len(job.Stack) == 0}
		now := s.nodes.clock.Now()
		if progress.Done {
			job.FinishedAt = now.UTC()
		}
		if err := writeDeleteJob(ctx, tr, job, now); err != nil {
			return err
		}
		return changes.append(ctx, tr)
	})
	if err != nil {
		return node.SubtreeDeleteProgress{Deleted: 0, Moved: 0, Done: false}, err
	}
	return progress, nil
}

// advance examines the last node on the stack of job, changes job to the
// state after the step, and records each changed record in changes.
func (s *NodeDeleteStore) advance(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, changes *mutationAudit) error {
	topID := job.Stack[len(job.Stack)-1]
	kinds := make(map[string]*node.NodeType)
	resolved, err := readHierarchyNodes(ctx, tr, job.OrgID, []uuid.UUID{topID}, kinds)
	if err != nil {
		return err
	}
	top := resolved[0]
	if !top.Found {
		job.Stack = job.Stack[:len(job.Stack)-1]
		return nil
	}
	incoming, err := readDeleteEdges(ctx, tr, job.OrgID, topID, "", true)
	if err != nil {
		return err
	}
	outgoing, err := readDeleteEdges(ctx, tr, job.OrgID, topID, "", false)
	if err != nil {
		return err
	}
	examined := incoming[:min(len(incoming), maxDeleteEdgePage)]
	child, others, err := findHierarchyChild(ctx, tr, job, top, examined, kinds)
	if err != nil {
		return err
	}
	if child.Found {
		if err := s.takeChild(ctx, tr, job, child, kinds, changes); err != nil {
			return err
		}
		return s.clearDeleteEdges(ctx, tr, job.OrgID, others, changes)
	}
	if len(incoming) > maxDeleteEdgePage || len(outgoing) > maxDeleteEdgePage {
		page := make([]deleteEdge, 0, len(others)+maxDeleteEdgePage)
		page = append(page, others...)
		page = append(page, outgoing[:min(len(outgoing), maxDeleteEdgePage)]...)
		return s.clearDeleteEdges(ctx, tr, job.OrgID, page, changes)
	}
	return s.deleteTop(ctx, tr, job, top, changes)
}

// takeChild moves a direct child of the root to the root's parent when
// moveChild accepts it. Every other child joins the stack and is deleted with
// its descendants.
func (s *NodeDeleteStore) takeChild(
	ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, child hierarchyNode,
	kinds map[string]*node.NodeType, changes *mutationAudit,
) error {
	moved := false
	if len(job.Stack) == 1 {
		var err error
		moved, err = s.moveChild(ctx, tr, job, child, kinds, changes)
		if err != nil {
			return err
		}
	}
	if moved {
		job.Moved++
		return nil
	}
	job.Stack = append(job.Stack, child.ID)
	return nil
}

// findHierarchyChild returns the source of the first edge in edges that is a
// hierarchy child of top: a child_of edge from a node with top as its
// hierarchy parent under [node.HierarchyParent]. It also returns every edge
// before that child with a source that is not a hierarchy child. A node
// already on the stack is never a child, and the stack never contains one
// node twice. The returned child has Found false when edges contain no child.
func findHierarchyChild(
	ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, top hierarchyNode, edges []deleteEdge, kinds map[string]*node.NodeType,
) (hierarchyNode, []deleteEdge, error) {
	none := hierarchyNode{ID: uuid.Nil, TypeKey: "", Kind: nil, Found: false}
	sourceIDs := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		sourceIDs = append(sourceIDs, edge.SourceID)
	}
	sources, err := readHierarchyNodes(ctx, tr, job.OrgID, sourceIDs, kinds)
	if err != nil {
		return none, nil, err
	}
	others := make([]deleteEdge, 0, len(edges))
	for position, source := range sources {
		isChild := edges[position].RelationType == node.RelChildOf && source.Found && !slices.Contains(job.Stack, source.ID)
		if isChild {
			isChild, err = hasHierarchyParent(ctx, tr, job.OrgID, source, top.ID)
			if err != nil {
				return none, nil, err
			}
		}
		if isChild {
			return source, others, nil
		}
		others = append(others, edges[position])
	}
	return none, others, nil
}
