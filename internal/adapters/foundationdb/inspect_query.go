package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// NewInspectStore returns a read-only inspector over the shared FDB handle.
func NewInspectStore(db fdb.Database) *InspectStore {
	return &InspectStore{db: db}
}

// QueryNodeRecords returns every core record-family row that currently matches
// nodeID. Families without a nodeID-addressable prefix are scanned and filtered.
func (s *InspectStore) QueryNodeRecords(_ context.Context, nodeID uuid.UUID) (report *NodeInspectionReport, err error) {
	report = &NodeInspectionReport{
		NodeID:                  nodeID,
		Resolve:                 nil,
		NodeInstances:           nil,
		NodeViews:               nil,
		PropertyIndexRows:       nil,
		LegacyAddressRows:       nil,
		Relationships:           nil,
		RelationshipReverseRows: nil,
	}
	_, err = s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		if resolveBytes, readErr := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get(); readErr != nil {
			return nil, fmt.Errorf("read node_resolve for %s: %w", nodeID, readErr)
		} else if len(resolveBytes) > 0 {
			var resolve node.NodeResolve
			if err := json.Unmarshal(resolveBytes, &resolve); err != nil {
				return nil, fmt.Errorf("unmarshal node_resolve for %s: %w", nodeID, err)
			}
			report.Resolve = &resolve
		}
		instances, err := queryNodeInstances(tr, nodeID)
		if err != nil {
			return nil, err
		}
		report.NodeInstances = instances
		views, err := queryNodeViews(tr, nodeID)
		if err != nil {
			return nil, err
		}
		report.NodeViews = views
		propertyRows, err := queryPropertyRows(tr, nodeID)
		if err != nil {
			return nil, err
		}
		report.PropertyIndexRows = propertyRows
		legacyAddressRows, err := queryLegacyAddressRows(tr, nodeID)
		if err != nil {
			return nil, err
		}
		report.LegacyAddressRows = legacyAddressRows
		orgIDs := collectOrgIDs(report)
		relRows, reverseRows, err := queryRelationshipRows(tr, nodeID, orgIDs)
		if err != nil {
			return nil, err
		}
		report.Relationships = relRows
		report.RelationshipReverseRows = reverseRows
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect node %s: %w", nodeID, err)
	}
	sortInspectionReport(report)
	return report, nil
}

func queryNodeInstances(tr fdb.ReadTransaction, nodeID uuid.UUID) ([]InspectNodeInstance, error) {
	kvs, err := scanPrefix(tr, []any{keyNodeInstance})
	if err != nil {
		return nil, fmt.Errorf("scan node_instance: %w", err)
	}
	out := make([]InspectNodeInstance, 0)
	for _, kv := range kvs {
		t, err := tuple.Unpack(stripPrefix(kv.Key))
		if err != nil || len(t) != 4 {
			continue
		}
		rowNodeID, err := uuid.Parse(asString(t[3]))
		if err != nil || rowNodeID != nodeID {
			continue
		}
		var stored node.Node
		if err := json.Unmarshal(kv.Value, &stored); err != nil {
			return nil, fmt.Errorf("unmarshal node_instance for %s: %w", nodeID, err)
		}
		out = append(out, InspectNodeInstance{OrgID: parseUUID(t[1]), NodeType: asString(t[2]), NodeID: rowNodeID, Value: &stored})
	}
	return out, nil
}

func queryNodeViews(tr fdb.ReadTransaction, nodeID uuid.UUID) ([]InspectNodeView, error) {
	kvs, err := scanPrefix(tr, []any{keyNodeView})
	if err != nil {
		return nil, fmt.Errorf("scan node_view: %w", err)
	}
	out := make([]InspectNodeView, 0)
	for _, kv := range kvs {
		t, err := tuple.Unpack(stripPrefix(kv.Key))
		if err != nil || len(t) != 4 {
			continue
		}
		rowNodeID, err := uuid.Parse(asString(t[3]))
		if err != nil || rowNodeID != nodeID {
			continue
		}
		var stored node.NodeView
		if err := json.Unmarshal(kv.Value, &stored); err != nil {
			return nil, fmt.Errorf("unmarshal node_view for %s: %w", nodeID, err)
		}
		out = append(out, InspectNodeView{OrgID: parseUUID(t[1]), NodeType: asString(t[2]), NodeID: rowNodeID, Value: &stored})
	}
	return out, nil
}

func queryPropertyRows(tr fdb.ReadTransaction, nodeID uuid.UUID) ([]InspectPropertyIndexRow, error) {
	kvs, err := scanPrefix(tr, []any{keyNodeByProperty})
	if err != nil {
		return nil, fmt.Errorf("scan node_by_property: %w", err)
	}
	out := make([]InspectPropertyIndexRow, 0)
	for _, kv := range kvs {
		t, err := tuple.Unpack(stripPrefix(kv.Key))
		if err != nil || len(t) != 6 {
			continue
		}
		rowNodeID, err := uuid.Parse(asString(t[5]))
		if err != nil || rowNodeID != nodeID {
			continue
		}
		out = append(out, InspectPropertyIndexRow{
			OrgID:        parseUUID(t[1]),
			NodeType:     asString(t[2]),
			PropertyName: asString(t[3]),
			EncodedValue: encodedValueBytes(t[4]),
			NodeID:       rowNodeID,
		})
	}
	return out, nil
}

func queryLegacyAddressRows(tr fdb.ReadTransaction, nodeID uuid.UUID) ([]InspectLegacyAddressRow, error) {
	kvs, err := scanPrefix(tr, []any{legacySlugIndexKeyFamily})
	if err != nil {
		return nil, err
	}
	out := make([]InspectLegacyAddressRow, 0)
	for _, kv := range kvs {
		ownerID, err := uuid.FromBytes(kv.Value)
		if err != nil || ownerID != nodeID {
			continue
		}
		t, err := tuple.Unpack(stripPrefix(kv.Key))
		if err != nil || len(t) != 3 {
			continue
		}
		out = append(out, InspectLegacyAddressRow{
			LegacyKeyFamily: legacySlugIndexKeyFamily,
			NodeType:        asString(t[1]),
			AddressKind:     "slug",
			AddressValue:    asString(t[2]),
			OwnerID:         ownerID,
		})
	}
	return out, nil
}
