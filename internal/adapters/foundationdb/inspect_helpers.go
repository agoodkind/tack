package foundationdb

import (
	"sort"
	"strings"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

func queryRelationshipRows(
	transaction fdb.ReadTransaction,
	nodeID uuid.UUID,
	orgIDs []uuid.UUID,
) ([]InspectRelationshipRow, []InspectRelationshipReverseRow, error) {
	forwardRows := make([]InspectRelationshipRow, 0)
	reverseRows := make([]InspectRelationshipReverseRow, 0)

	for _, orgID := range orgIDs {
		forward, err := queryForwardRelationshipRows(transaction, nodeID, orgID)
		if err != nil {
			return nil, nil, err
		}

		forwardRows = append(forwardRows, forward...)
		reverse, err := queryReverseRelationshipRows(transaction, nodeID, orgID)
		if err != nil {
			return nil, nil, err
		}

		reverseRows = append(reverseRows, reverse...)
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

	sort.Slice(
		orgIDs,
		func(i int, j int) bool {
			return orgIDs[i].String() < orgIDs[j].String()
		},
	)

	return orgIDs
}

func sortInspectionReport(report *NodeInspectionReport) {
	sort.Slice(
		report.NodeInstances,
		func(i int, j int) bool {
			return rowKey(
				report.NodeInstances[i].OrgID.String(),
				report.NodeInstances[i].NodeType,
			) < rowKey(report.NodeInstances[j].OrgID.String(), report.NodeInstances[j].NodeType)
		},
	)
	sort.Slice(
		report.NodeViews,
		func(i int, j int) bool {
			return rowKey(
				report.NodeViews[i].OrgID.String(),
				report.NodeViews[i].NodeType,
			) < rowKey(report.NodeViews[j].OrgID.String(), report.NodeViews[j].NodeType)
		},
	)
	sort.Slice(
		report.PropertyIndexRows,
		func(i int, rightIndex int) bool {
			left := rowKey(
				report.PropertyIndexRows[i].OrgID.String(),
				report.PropertyIndexRows[i].NodeType,
				report.PropertyIndexRows[i].PropertyName,
				string(report.PropertyIndexRows[i].EncodedValue),
			)
			right := rowKey(
				report.PropertyIndexRows[rightIndex].OrgID.String(),
				report.PropertyIndexRows[rightIndex].NodeType,
				report.PropertyIndexRows[rightIndex].PropertyName,
				string(report.PropertyIndexRows[rightIndex].EncodedValue),
			)

			return left < right
		},
	)
	sort.Slice(
		report.Relationships,
		func(i int, rightIndex int) bool {
			left := rowKey(
				report.Relationships[i].OrgID.String(),
				report.Relationships[i].RelationType,
				report.Relationships[i].TargetID.String(),
			)
			right := rowKey(
				report.Relationships[rightIndex].OrgID.String(),
				report.Relationships[rightIndex].RelationType,
				report.Relationships[rightIndex].TargetID.String(),
			)

			return left < right
		},
	)
	sort.Slice(
		report.RelationshipReverseRows,
		func(i int, rightIndex int) bool {
			left := rowKey(
				report.RelationshipReverseRows[i].OrgID.String(),
				report.RelationshipReverseRows[i].RelationType,
				report.RelationshipReverseRows[i].SourceID.String(),
			)
			right := rowKey(
				report.RelationshipReverseRows[rightIndex].OrgID.String(),
				report.RelationshipReverseRows[rightIndex].RelationType,
				report.RelationshipReverseRows[rightIndex].SourceID.String(),
			)

			return left < right
		},
	)
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
