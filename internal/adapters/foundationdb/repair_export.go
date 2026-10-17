package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

// RepairExportRecord contains a node's resolution, primary, and view records
// and the stored forward value for each relationship key to or from the node.
type RepairExportRecord struct {
	Found         bool
	Resolve       json.RawMessage
	Primary       json.RawMessage
	View          json.RawMessage
	Relationships []RepairExportEdge
}

// RepairExportEdge pairs a relationship key for the exported node with its
// stored forward value. Value is nil if the forward value does not exist.
type RepairExportEdge struct {
	SourceID     uuid.UUID       `json:"source_id"`
	RelationType string          `json:"relation_type"`
	TargetID     uuid.UUID       `json:"target_id"`
	Value        json.RawMessage `json:"value"`
}

// RepairExport reads nodeID's resolution, primary, and view records and the
// stored forward value for every relationship key to or from the node in one
// read transaction. It returns an error if the edges in either direction
// exceed one page.
func (s *RepairStore) RepairExport(ctx context.Context, nodeID uuid.UUID) (record RepairExportRecord, err error) {
	defer telemetry.FDBOp(ctx, "store.repair.export")(&err)
	err = runNodeReadTransaction(ctx, s.db, "read repair export of node "+nodeID.String(), func(tr fdb.Transaction) error {
		var readErr error
		record, readErr = readRepairExport(ctx, tr, nodeID)
		return readErr
	})
	return record, err
}

func readRepairExport(ctx context.Context, tr fdb.Transaction, nodeID uuid.UUID) (RepairExportRecord, error) {
	record := RepairExportRecord{Found: false, Resolve: nil, Primary: nil, View: nil, Relationships: nil}
	current, err := readRepairNode(ctx, tr, nodeID)
	if err != nil || !current.Found {
		return record, err
	}
	resolve, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return record, searchReadFailure(ctx, "read resolution of node "+nodeID.String(), err)
	}
	view, err := tr.Get(fdb.Key(nodeViewKey(current.OrgID, current.NodeType, nodeID))).Get()
	if err != nil {
		return record, searchReadFailure(ctx, "read view of node "+nodeID.String(), err)
	}
	record.Found, record.Resolve, record.Primary = true, resolve, current.Primary
	if len(view) > 0 {
		record.View = view
	}
	record.Relationships = []RepairExportEdge{}
	for _, incoming := range []bool{false, true} {
		edges, err := readRepairEdges(ctx, tr, current, incoming)
		if err != nil {
			return record, err
		}
		for _, edge := range edges {
			stored, err := tr.Get(fdb.Key(relationshipKey(current.OrgID, edge.SourceID, edge.RelationType, edge.TargetID))).Get()
			if err != nil {
				return record, searchReadFailure(ctx, "read relationship of node "+nodeID.String(), err)
			}
			exported := RepairExportEdge{SourceID: edge.SourceID, RelationType: edge.RelationType, TargetID: edge.TargetID, Value: nil}
			if len(stored) > 0 {
				exported.Value = stored
			}
			record.Relationships = append(record.Relationships, exported)
		}
	}
	return record, nil
}
