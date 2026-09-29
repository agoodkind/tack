package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
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
// finished job and a job record that no longer exists report Done.
func (s *NodeDeleteStore) DeleteSubtreeStep(ctx context.Context, jobID uuid.UUID, events node.DeletionEventBuilder) (progress node.SubtreeDeleteProgress, err error) {
	defer telemetry.FDBOp(ctx, "store.node.subtree_delete_step")(&err)
	rootDeleted := false
	err = runNodeMutation(ctx, s.nodes.db, "subtree delete step of job "+jobID.String(), func(tr fdb.Transaction) error {
		rootDeleted = false
		progress = node.SubtreeDeleteProgress{Deleted: 0, Done: true}
		job, err := readDeleteJob(ctx, tr, jobID)
		if err != nil || job == nil {
			return err
		}
		if len(job.Stack) > 0 {
			rootDeleted, err = s.advance(ctx, tr, job, events)
			if err != nil {
				return err
			}
		}
		progress = node.SubtreeDeleteProgress{Deleted: job.Deleted, Done: len(job.Stack) == 0}
		if job.Finished() {
			return nil
		}
		now := s.nodes.clock.Now()
		if progress.Done {
			job.FinishedAt = now.UTC()
		}
		return writeDeleteJob(ctx, tr, job, now)
	})
	if err != nil {
		return node.SubtreeDeleteProgress{Deleted: 0, Done: false}, err
	}
	if rootDeleted {
		commitStagedIntent(ctx)
	}
	return progress, nil
}

// advance examines the last node on the stack of job and changes job to the
// state after the step. It reports whether the step deleted the root.
func (s *NodeDeleteStore) advance(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, events node.DeletionEventBuilder) (bool, error) {
	topID := job.Stack[len(job.Stack)-1]
	kinds := make(map[string]*node.NodeType)
	resolved, err := readHierarchyNodes(ctx, tr, job.OrgID, []uuid.UUID{topID}, kinds)
	if err != nil {
		return false, err
	}
	top := resolved[0]
	if !top.Found {
		job.Stack = job.Stack[:len(job.Stack)-1]
		return false, nil
	}
	incoming, err := readDeleteEdges(ctx, tr, job.OrgID, topID, true, maxDeleteEdgePage+1)
	if err != nil {
		return false, err
	}
	outgoing, err := readDeleteEdges(ctx, tr, job.OrgID, topID, false, maxDeleteEdgePage+1)
	if err != nil {
		return false, err
	}
	examined := incoming[:min(len(incoming), maxDeleteEdgePage)]
	childID, others, err := findHierarchyChild(ctx, tr, job, top, examined, kinds)
	if err != nil {
		return false, err
	}
	if childID != uuid.Nil {
		job.Stack = append(job.Stack, childID)
		return false, s.clearDeleteEdges(ctx, tr, job.OrgID, others)
	}
	if len(incoming) > maxDeleteEdgePage || len(outgoing) > maxDeleteEdgePage {
		page := make([]deleteEdge, 0, len(others)+maxDeleteEdgePage)
		page = append(page, others...)
		page = append(page, outgoing[:min(len(outgoing), maxDeleteEdgePage)]...)
		return false, s.clearDeleteEdges(ctx, tr, job.OrgID, page)
	}
	return s.deleteTop(ctx, tr, job, top, events)
}

// findHierarchyChild returns the source of the first edge in edges that is a
// hierarchy child of top. It also returns every edge with a source that is
// not a hierarchy child. A node already on the stack is never a child, and
// the stack never contains one node twice. It returns uuid.Nil when edges
// contain no child.
func findHierarchyChild(
	ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, top hierarchyNode, edges []deleteEdge, kinds map[string]*node.NodeType,
) (uuid.UUID, []deleteEdge, error) {
	sourceIDs := make([]uuid.UUID, 0, len(edges))
	for _, edge := range edges {
		sourceIDs = append(sourceIDs, edge.SourceID)
	}
	sources, err := readHierarchyNodes(ctx, tr, job.OrgID, sourceIDs, kinds)
	if err != nil {
		return uuid.Nil, nil, err
	}
	childID := uuid.Nil
	others := make([]deleteEdge, 0, len(edges))
	for position, source := range sources {
		isChild := source.Found && !slices.Contains(job.Stack, source.ID) && node.LivesUnder(source.Kind, top.Kind)
		if isChild && childID == uuid.Nil {
			childID = source.ID
		}
		if !isChild {
			others = append(others, edges[position])
		}
	}
	return childID, others, nil
}

// deleteTop deletes the last node on the stack of job, writes its ledger
// event, and removes it from the stack. It reports whether the node is the
// root of the job. A resolution record without a node record deletes the
// resolution record, the edges, and the references of the node.
func (s *NodeDeleteStore) deleteTop(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, top hierarchyNode, events node.DeletionEventBuilder) (bool, error) {
	encoded, err := tr.Get(fdb.Key(nodeInstanceKey(job.OrgID, top.TypeKey, top.ID))).Get()
	if err != nil {
		return false, searchReadFailure(ctx, "read node "+top.ID.String(), err)
	}
	current := node.Node{
		ID: top.ID, OrgID: job.OrgID, NodeType: top.TypeKey, Name: "", Props: map[string]json.RawMessage{},
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: job.UpdatedAt, UpdatedAt: job.UpdatedAt,
	}
	if len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &current); err != nil {
			return false, nodeOperationFailure(ctx, "decode node "+top.ID.String(), err)
		}
	}
	if err := s.nodes.clearNode(ctx, tr, &current); err != nil {
		return false, err
	}
	isRoot := top.ID == job.RootID
	if err := writeDeleteEvent(ctx, tr, job, &current, isRoot, events); err != nil {
		return false, err
	}
	job.Stack = job.Stack[:len(job.Stack)-1]
	job.Deleted++
	return isRoot, nil
}

// writeDeleteEvent writes the ledger event of one deleted node to the
// operator outbox inside tr. The root writes the job's template unchanged. A
// descendant writes the event that events builds from the template. A job
// without a template writes no event.
func writeDeleteEvent(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, deleted *node.Node, isRoot bool, events node.DeletionEventBuilder) error {
	if len(job.AuditTemplate) == 0 {
		return nil
	}
	payload := job.AuditTemplate
	if !isRoot {
		if events == nil {
			return nodeOperationFailure(ctx, "build the ledger event of node "+deleted.ID.String(), errors.New("no ledger event builder"))
		}
		built, err := events(job.AuditTemplate, node.DeletedNode{ID: deleted.ID, NodeType: deleted.NodeType, Name: deleted.Name})
		if err != nil {
			return nodeOperationFailure(ctx, "build the ledger event of node "+deleted.ID.String(), err)
		}
		payload = built
	}
	key, err := marshalOpsOutboxVersionstampedKey()
	if err != nil {
		return nodeOperationFailure(ctx, "pack the ledger event key of node "+deleted.ID.String(), err)
	}
	tr.SetVersionstampedKey(key, payload)
	return nil
}
