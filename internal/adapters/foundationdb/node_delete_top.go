package foundationdb

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

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
	if _, err := s.nodes.fenceAttempt(ctx, tr, job.OrgID, top.ID); err != nil {
		return false, err
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
	if err := NewOutboxWriter(tr).WriteEventJSON(ctx, payload); err != nil {
		return nodeOperationFailure(ctx, "write the ledger event of node "+change.ID.String(), err)
	}
	return nil
}
