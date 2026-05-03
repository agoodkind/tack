package foundationdb

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func queryRelationshipRows(tr fdb.ReadTransaction, nodeID uuid.UUID, orgIDs []uuid.UUID) ([]InspectRelationshipRow, []InspectRelationshipReverseRow, error) {
	forwardRows := make([]InspectRelationshipRow, 0)
	reverseRows := make([]InspectRelationshipReverseRow, 0)
	for _, orgID := range orgIDs {
		forwardPrefix := relationshipPrefixBySource(orgID, nodeID, "")
		forwardRange, err := fdb.PrefixRange(forwardPrefix)
		if err != nil {
			return nil, nil, fmt.Errorf("prefix relationship source %s: %w", nodeID, err)
		}
		forwardKVs, err := tr.GetRange(forwardRange, fdb.RangeOptions{}).GetSliceWithError()
		if err != nil {
			return nil, nil, fmt.Errorf("scan relationship source %s: %w", nodeID, err)
		}
		for _, kv := range forwardKVs {
			t, err := tuple.Unpack(stripPrefix(kv.Key))
			if err != nil || len(t) != 5 {
				continue
			}
			targetID, err := uuid.Parse(asString(t[4]))
			if err != nil {
				continue
			}
			reverseValue, err := tr.Get(fdb.Key(relationshipReverseKey(orgID, targetID, asString(t[3]), nodeID))).Get()
			if err != nil {
				return nil, nil, fmt.Errorf("read relationship reverse companion %s -> %s: %w", nodeID, targetID, err)
			}
			var rel node.Relationship
			if err := json.Unmarshal(kv.Value, &rel); err != nil {
				return nil, nil, fmt.Errorf("unmarshal relationship %s -> %s: %w", nodeID, targetID, err)
			}
			forwardRows = append(forwardRows, InspectRelationshipRow{
				OrgID:             orgID,
				SourceID:          nodeID,
				RelationType:      asString(t[3]),
				TargetID:          targetID,
				ReverseRowPresent: reverseValue != nil,
				Value:             &rel,
			})
		}
		reversePrefix := relationshipReversePrefixByTarget(orgID, nodeID, "")
		reverseRange, err := fdb.PrefixRange(reversePrefix)
		if err != nil {
			return nil, nil, fmt.Errorf("prefix relationship_reverse target %s: %w", nodeID, err)
		}
		reverseKVs, err := tr.GetRange(reverseRange, fdb.RangeOptions{}).GetSliceWithError()
		if err != nil {
			return nil, nil, fmt.Errorf("scan relationship_reverse target %s: %w", nodeID, err)
		}
		for _, kv := range reverseKVs {
			t, err := tuple.Unpack(stripPrefix(kv.Key))
			if err != nil || len(t) != 5 {
				continue
			}
			sourceID, err := uuid.Parse(asString(t[4]))
			if err != nil {
				continue
			}
			forwardValue, err := tr.Get(fdb.Key(relationshipKey(orgID, sourceID, asString(t[3]), nodeID))).Get()
			if err != nil {
				return nil, nil, fmt.Errorf("read relationship forward companion %s -> %s: %w", sourceID, nodeID, err)
			}
			reverseRows = append(reverseRows, InspectRelationshipReverseRow{
				OrgID:             orgID,
				TargetID:          nodeID,
				RelationType:      asString(t[3]),
				SourceID:          sourceID,
				ForwardRowPresent: forwardValue != nil,
			})
		}
	}
	return forwardRows, reverseRows, nil
}

func collectOrgIDs(report *NodeInspectionReport) []uuid.UUID {
	orgMap := map[uuid.UUID]struct{}{}
	if report.Resolve != nil && report.Resolve.OrgID != uuid.Nil {
		orgMap[report.Resolve.OrgID] = struct{}{}
	}
	for _, row := range report.NodeInstances {
		if row.OrgID != uuid.Nil {
			orgMap[row.OrgID] = struct{}{}
		}
	}
	for _, row := range report.NodeViews {
		if row.OrgID != uuid.Nil {
			orgMap[row.OrgID] = struct{}{}
		}
	}
	for _, row := range report.PropertyIndexRows {
		if row.OrgID != uuid.Nil {
			orgMap[row.OrgID] = struct{}{}
		}
	}
	orgIDs := make([]uuid.UUID, 0, len(orgMap))
	for orgID := range orgMap {
		orgIDs = append(orgIDs, orgID)
	}
	sort.Slice(orgIDs, func(i int, j int) bool {
		return orgIDs[i].String() < orgIDs[j].String()
	})
	return orgIDs
}

func sortInspectionReport(report *NodeInspectionReport) {
	sort.Slice(report.NodeInstances, func(i int, j int) bool {
		return rowKey(report.NodeInstances[i].OrgID.String(), report.NodeInstances[i].NodeType) < rowKey(report.NodeInstances[j].OrgID.String(), report.NodeInstances[j].NodeType)
	})
	sort.Slice(report.NodeViews, func(i int, j int) bool {
		return rowKey(report.NodeViews[i].OrgID.String(), report.NodeViews[i].NodeType) < rowKey(report.NodeViews[j].OrgID.String(), report.NodeViews[j].NodeType)
	})
	sort.Slice(report.PropertyIndexRows, func(i int, j int) bool {
		left := rowKey(report.PropertyIndexRows[i].OrgID.String(), report.PropertyIndexRows[i].NodeType, report.PropertyIndexRows[i].PropertyName, string(report.PropertyIndexRows[i].EncodedValue))
		right := rowKey(report.PropertyIndexRows[j].OrgID.String(), report.PropertyIndexRows[j].NodeType, report.PropertyIndexRows[j].PropertyName, string(report.PropertyIndexRows[j].EncodedValue))
		return left < right
	})
	sort.Slice(report.SlugRows, func(i int, j int) bool {
		return rowKey(report.SlugRows[i].NodeType, report.SlugRows[i].Slug) < rowKey(report.SlugRows[j].NodeType, report.SlugRows[j].Slug)
	})
	sort.Slice(report.Relationships, func(i int, j int) bool {
		left := rowKey(report.Relationships[i].OrgID.String(), report.Relationships[i].RelationType, report.Relationships[i].TargetID.String())
		right := rowKey(report.Relationships[j].OrgID.String(), report.Relationships[j].RelationType, report.Relationships[j].TargetID.String())
		return left < right
	})
	sort.Slice(report.RelationshipReverseRows, func(i int, j int) bool {
		left := rowKey(report.RelationshipReverseRows[i].OrgID.String(), report.RelationshipReverseRows[i].RelationType, report.RelationshipReverseRows[i].SourceID.String())
		right := rowKey(report.RelationshipReverseRows[j].OrgID.String(), report.RelationshipReverseRows[j].RelationType, report.RelationshipReverseRows[j].SourceID.String())
		return left < right
	})
}

func scanPrefix(tr fdb.ReadTransaction, elements []any) ([]fdb.KeyValue, error) {
	packed := make(tuple.Tuple, 0, len(elements))
	for _, element := range elements {
		packed = append(packed, element)
	}
	prefix := withPrefix(packed.Pack())
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func parseUUID(value any) uuid.UUID {
	id, err := uuid.Parse(asString(value))
	if err != nil {
		return uuid.Nil
	}
	return id
}

func encodedValueBytes(value any) []byte {
	raw, ok := value.([]byte)
	if !ok {
		return nil
	}
	copied := make([]byte, len(raw))
	copy(copied, raw)
	return copied
}

func rowKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}
