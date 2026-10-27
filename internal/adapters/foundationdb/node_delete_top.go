package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// deleteTop deletes the last node on job.Stack and removes it from the stack.
// It records deletions of stored nodes and relationships in changes. When the
// node record does not exist, deleteTop still deletes the
// resolution record, the edges, and the references of the node.
func (s *NodeDeleteStore) deleteTop(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, top hierarchyNode, changes *mutationAudit) error {
	encoded, err := tr.Get(fdb.Key(nodeInstanceKey(job.OrgID, top.TypeKey, top.ID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read node "+top.ID.String(), err)
	}
	current := node.Node{
		ID: top.ID, OrgID: job.OrgID, NodeType: top.TypeKey, Name: "", Props: map[string]json.RawMessage{},
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: job.UpdatedAt, UpdatedAt: job.UpdatedAt,
	}
	if len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &current); err != nil {
			return nodeOperationFailure(ctx, "decode node "+top.ID.String(), err)
		}
	}
	if _, err := s.nodes.fenceAttempt(ctx, tr, job.OrgID, top.ID); err != nil {
		return err
	}
	if err := s.nodes.clearNode(ctx, tr, &current, changes); err != nil {
		return err
	}
	job.Stack = job.Stack[:len(job.Stack)-1]
	job.Deleted++
	return nil
}
