package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// maxSummaryBatch bounds the node IDs one summary operation reads.
const maxSummaryBatch = 100

// NodeSummaryStore reads current node identity from FoundationDB and
// compiles each node's current opaque access keys.
type NodeSummaryStore struct {
	db       fdb.Database
	policies *searchaccess.PolicySet
}

var _ searchdomain.SummaryReader = (*NodeSummaryStore)(nil)

// NewNodeSummaryStore creates the node summary reader.
func NewNodeSummaryStore(db fdb.Database, policies *searchaccess.PolicySet) *NodeSummaryStore {
	return &NodeSummaryStore{db: db, policies: policies}
}

// Summaries returns one result per requested ID in input order. One read
// transaction reads every view and compiles every node's current access keys
// from its current ancestry. It returns a deleted result for a node without a
// resolve record or view. A node with a hierarchy defect is found with no
// access keys, and no caller key permits it. A failed read returns an error.
func (s *NodeSummaryStore) Summaries(ctx context.Context, nodeIDs []uuid.UUID, maxNameBytes int) (results []node.SummaryResult, err error) {
	defer telemetry.FDBOp(ctx, "store.node_summary.read")(&err)
	if len(nodeIDs) > maxSummaryBatch || maxNameBytes < 1 {
		return nil, summaryFailure(ctx, fmt.Errorf("%d node IDs or name bound %d are outside the summary bounds", len(nodeIDs), maxNameBytes))
	}
	views := make([]*node.NodeView, len(nodeIDs))
	var accesses map[searchaccess.OrgNode]searchaccess.ResourceAccess
	err = runNodeReadTransaction(ctx, s.db, "read node summaries", func(tr fdb.Transaction) error {
		if readErr := readSummaryViews(tr, nodeIDs, views); readErr != nil {
			return readErr
		}
		resources := make([]searchaccess.OrgNode, 0, len(nodeIDs))
		for position, nodeID := range nodeIDs {
			if views[position] != nil {
				resources = append(resources, searchaccess.OrgNode{OrgID: views[position].OrgID, NodeID: nodeID})
			}
		}
		var accessErr error
		accesses, accessErr = s.policies.BatchResourceKeys(ctx, newSummaryHierarchy(tr), resources)
		if accessErr != nil {
			return searchReadFailure(ctx, "compile current access of the summary batch", accessErr)
		}
		return nil
	})
	if err != nil {
		return nil, summaryFailure(ctx, err)
	}
	results = make([]node.SummaryResult, 0, len(nodeIDs))
	for position, nodeID := range nodeIDs {
		view := views[position]
		if view == nil {
			results = append(results, node.SummaryResult{NodeID: nodeID, Status: node.SummaryDeleted, Summary: node.Summary{ID: nodeID, NodeType: "", Name: ""}, AccessKeys: nil})
			continue
		}
		access := accesses[searchaccess.OrgNode{OrgID: view.OrgID, NodeID: nodeID}]
		if access.Defect != nil {
			telemetry.L(ctx).ErrorContext(ctx, "search.summary.withheld",
				slog.String("node_id", nodeID.String()), slog.String("reason", access.Defect.Error()))
		}
		summary := node.Summary{ID: nodeID, NodeType: view.NodeType, Name: node.TruncateUTF8(view.Name, maxNameBytes)}
		results = append(results, node.SummaryResult{NodeID: nodeID, Status: node.SummaryFound, Summary: summary, AccessKeys: access.Keys})
	}
	return results, nil
}

// readSummaryViews issues every resolve read before it waits on any of them.
// It then waits on each resolve read in order and issues that node's view
// read after the resolve record decodes. It waits on the view reads after it
// has issued all of them.
func readSummaryViews(tr fdb.Transaction, nodeIDs []uuid.UUID, views []*node.NodeView) error {
	resolves := make([]fdb.FutureByteSlice, len(nodeIDs))
	for position, nodeID := range nodeIDs {
		resolves[position] = tr.Get(fdb.Key(nodeResolveKey(nodeID)))
	}
	pending := make([]fdb.FutureByteSlice, len(nodeIDs))
	for position, future := range resolves {
		encoded, err := future.Get()
		if err != nil {
			return sessionStepError{operation: "read resolve record of node " + nodeIDs[position].String(), err: err}
		}
		if len(encoded) == 0 {
			continue
		}
		var resolved node.NodeResolve
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return sessionStepError{operation: "decode resolve record of node " + nodeIDs[position].String(), err: err}
		}
		pending[position] = tr.Get(fdb.Key(nodeViewKey(resolved.OrgID, resolved.NodeType, nodeIDs[position])))
	}
	for position, future := range pending {
		views[position] = nil
		if future == nil {
			continue
		}
		encoded, err := future.Get()
		if err != nil {
			return sessionStepError{operation: "read view of node " + nodeIDs[position].String(), err: err}
		}
		if len(encoded) == 0 {
			continue
		}
		var view node.NodeView
		if err := json.Unmarshal(encoded, &view); err != nil {
			return sessionStepError{operation: "decode view of node " + nodeIDs[position].String(), err: err}
		}
		views[position] = &view
	}
	return nil
}

func summaryFailure(ctx context.Context, err error) error {
	wrapped := fmt.Errorf("read node summaries: %w", err)
	if searchFailureWasLogged(err) {
		return wrapped
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.summary.read_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
