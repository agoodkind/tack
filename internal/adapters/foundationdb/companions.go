package foundationdb

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func queryForwardRelationshipRows(
	transaction fdb.ReadTransaction,
	nodeID uuid.UUID,
	orgID uuid.UUID,
) ([]InspectRelationshipRow, error) {
	forwardRows := make([]InspectRelationshipRow, 0)
	forwardPrefix := relationshipPrefixBySource(orgID, nodeID, "")
	forwardRange, err := fdb.PrefixRange(forwardPrefix)
	if err != nil {
		return nil, relationshipInspectionFailure(
			"prefix relationship source "+nodeID.String(),
			err,
		)
	}

	forwardKVs, err := transaction.GetRange(forwardRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, relationshipInspectionFailure("scan relationship source "+nodeID.String(), err)
	}

	for _, keyValue := range forwardKVs {
		keyTuple, err := tuple.Unpack(stripPrefix(keyValue.Key))
		if err != nil || len(keyTuple) != 5 {
			continue
		}

		targetID, err := uuid.Parse(asString(keyTuple[4]))
		if err != nil {
			continue
		}

		reverseValue, err := transaction.Get(
			fdb.Key(relationshipReverseKey(orgID, targetID, asString(keyTuple[3]), nodeID)),
		).Get()
		if err != nil {
			return nil, relationshipInspectionFailure(
				"read relationship reverse companion "+nodeID.String()+" -> "+targetID.String(),
				err,
			)
		}

		var rel node.Relationship
		if err := json.Unmarshal(keyValue.Value, &rel); err != nil {
			return nil, relationshipInspectionFailure(
				"unmarshal relationship "+nodeID.String()+" -> "+targetID.String(),
				err,
			)
		}

		forwardRows = append(
			forwardRows,
			InspectRelationshipRow{
				OrgID:             orgID,
				SourceID:          nodeID,
				RelationType:      asString(keyTuple[3]),
				TargetID:          targetID,
				ReverseRowPresent: reverseValue != nil,
				Value:             &rel,
			},
		)
	}

	return forwardRows, nil
}

func queryReverseRelationshipRows(
	transaction fdb.ReadTransaction,
	nodeID uuid.UUID,
	orgID uuid.UUID,
) ([]InspectRelationshipReverseRow, error) {
	reverseRows := make([]InspectRelationshipReverseRow, 0)
	reversePrefix := relationshipReversePrefixByTarget(orgID, nodeID, "")
	reverseRange, err := fdb.PrefixRange(reversePrefix)
	if err != nil {
		return nil, relationshipInspectionFailure(
			"prefix relationship_reverse target "+nodeID.String(),
			err,
		)
	}

	reverseKVs, err := transaction.GetRange(reverseRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, relationshipInspectionFailure(
			"scan relationship_reverse target "+nodeID.String(),
			err,
		)
	}

	for _, keyValue := range reverseKVs {
		keyTuple, err := tuple.Unpack(stripPrefix(keyValue.Key))
		if err != nil || len(keyTuple) != 5 {
			continue
		}

		sourceID, err := uuid.Parse(asString(keyTuple[4]))
		if err != nil {
			continue
		}

		forwardValue, err := transaction.Get(
			fdb.Key(relationshipKey(orgID, sourceID, asString(keyTuple[3]), nodeID)),
		).Get()
		if err != nil {
			return nil, relationshipInspectionFailure(
				"read relationship forward companion "+sourceID.String()+" -> "+nodeID.String(),
				err,
			)
		}

		reverseRows = append(
			reverseRows,
			InspectRelationshipReverseRow{
				OrgID:             orgID,
				TargetID:          nodeID,
				RelationType:      asString(keyTuple[3]),
				SourceID:          sourceID,
				ForwardRowPresent: forwardValue != nil,
			},
		)
	}

	return reverseRows, nil
}

func relationshipInspectionFailure(operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	slog.Error("inspect.relationships.failed", slog.String("err", wrapped.Error()))

	return wrapped
}
