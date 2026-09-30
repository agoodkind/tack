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
		progress = node.SubtreeDeleteProgress{Deleted: 0, Moved: 0, Done: true}
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
		progress = node.SubtreeDeleteProgress{Deleted: job.Deleted, Moved: job.Moved, Done: len(job.Stack) == 0}
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
		return node.SubtreeDeleteProgress{Deleted: 0, Moved: 0, Done: false}, err
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
	incoming, err := readDeleteEdges(ctx, tr, job.OrgID, topID, "", true)
	if err != nil {
		return false, err
	}
	outgoing, err := readDeleteEdges(ctx, tr, job.OrgID, topID, "", false)
	if err != nil {
		return false, err
	}
	examined := incoming[:min(len(incoming), maxDeleteEdgePage)]
	child, others, err := findHierarchyChild(ctx, tr, job, top, examined, kinds)
	if err != nil {
		return false, err
	}
	if child.Found {
		if err := s.takeChild(ctx, tr, job, child, kinds, events); err != nil {
			return false, err
		}
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

// takeChild moves a direct child of the root to the root's parent when
// moveChild accepts it. Every other child joins the stack and is deleted with
// its descendants.
func (s *NodeDeleteStore) takeChild(
	ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, child hierarchyNode,
	kinds map[string]*node.NodeType, events node.DeletionEventBuilder,
) error {
	moved := false
	if len(job.Stack) == 1 {
		var err error
		moved, err = s.moveChild(ctx, tr, job, child, kinds, events)
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
// hierarchy child of top: a child_of edge from a node that node.LivesUnder
// places under top. It also returns every edge with a source that is not a
// hierarchy child. A node already on the stack is never a child, and the
// stack never contains one node twice. The returned child has Found false
// when edges contain no child.
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
	child := none
	others := make([]deleteEdge, 0, len(edges))
	for position, source := range sources {
		isChild := edges[position].RelationType == node.RelChildOf && source.Found &&
			!slices.Contains(job.Stack, source.ID) && node.LivesUnder(source.Kind, top.Kind)
		if isChild && !child.Found {
			child = source
		}
		if !isChild {
			others = append(others, edges[position])
		}
	}
	return child, others, nil
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
	change := node.SubtreeChange{ID: current.ID, NodeType: current.NodeType, Name: current.Name, MovedFrom: uuid.Nil, MovedTo: uuid.Nil}
	if err := writeChangeEvent(ctx, tr, job, change, isRoot, events); err != nil {
		return false, err
	}
	job.Stack = job.Stack[:len(job.Stack)-1]
	job.Deleted++
	return isRoot, nil
}

// writeChangeEvent writes the ledger event of one deleted or moved node to
// the operator outbox inside tr. The root writes the job's template
// unchanged. Every other node writes the event that events builds from the
// template. A job without a template writes no event.
func writeChangeEvent(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, change node.SubtreeChange, isRoot bool, events node.DeletionEventBuilder) error {
	if len(job.AuditTemplate) == 0 {
		return nil
	}
	payload := job.AuditTemplate
	if !isRoot {
		if events == nil {
			return nodeOperationFailure(ctx, "build the ledger event of node "+change.ID.String(), errors.New("no ledger event builder"))
		}
		built, err := events(job.AuditTemplate, change)
		if err != nil {
			return nodeOperationFailure(ctx, "build the ledger event of node "+change.ID.String(), err)
		}
		payload = built
	}
	key, err := marshalOpsOutboxVersionstampedKey()
	if err != nil {
		return nodeOperationFailure(ctx, "pack the ledger event key of node "+change.ID.String(), err)
	}
	tr.SetVersionstampedKey(key, payload)
	return nil
}
