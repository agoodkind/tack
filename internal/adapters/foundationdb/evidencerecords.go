package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func readRepairNode(
	ctx context.Context,
	transaction fdb.Transaction,
	nodeID uuid.UUID,
) (RepairNodeRecord, error) {
	record := RepairNodeRecord{
		NodeID:   nodeID,
		Found:    false,
		OrgID:    uuid.Nil,
		NodeType: "",
		Primary:  nil,
		ParentID: uuid.Nil,
	}
	resolveBytes, err := transaction.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return record, searchReadFailure(ctx, "read resolution of node "+nodeID.String(), err)
	}

	if len(resolveBytes) == 0 {
		return record, nil
	}

	var resolve node.NodeResolve
	if err := json.Unmarshal(resolveBytes, &resolve); err != nil {
		return record, nodeOperationFailure(ctx, "decode resolution of node "+nodeID.String(), err)
	}

	primary, err := transaction.Get(
		fdb.Key(nodeInstanceKey(resolve.OrgID, resolve.NodeType, nodeID)),
	).Get()
	if err != nil {
		return record, searchReadFailure(ctx, "read primary record of node "+nodeID.String(), err)
	}

	if len(primary) == 0 {
		return record, nil
	}

	var stored node.Node
	if err := json.Unmarshal(primary, &stored); err != nil {
		return record, nodeOperationFailure(
			ctx,
			"decode primary record of node "+nodeID.String(),
			err,
		)
	}

	record.Found, record.OrgID, record.NodeType, record.Primary = true, resolve.OrgID, resolve.NodeType, primary
	var parentText string
	if json.Unmarshal(stored.Props[parentIDProperty], &parentText) == nil {
		if parentID, parseErr := uuid.Parse(parentText); parseErr == nil {
			record.ParentID = parentID
		}
	}

	return record, nil
}

func readRepairEdges(
	ctx context.Context,
	transaction fdb.Transaction,
	current RepairNodeRecord,
	incoming bool,
) ([]RepairEdgeRecord, error) {
	edges, err := readDeleteEdges(ctx, transaction, current.OrgID, current.NodeID, "", incoming)
	if err != nil {
		return nil, err
	}

	if len(edges) > maxDeleteEdgePage {
		wrapped := fmt.Errorf(
			"node %s has more than %d edges in one direction: %w",
			current.NodeID,
			maxDeleteEdgePage,
			domain.ErrFailedPrecondition,
		)
		slog.ErrorContext(
			ctx,
			"repair.evidence.edges_exceeded",
			slog.String("err", wrapped.Error()),
			slog.String("node_id", current.NodeID.String()),
		)

		return nil, loggedSearchError{err: wrapped}
	}

	records := make([]RepairEdgeRecord, 0, len(edges))
	for _, edge := range edges {
		records = append(records, RepairEdgeRecord(edge))
	}

	return records, nil
}
