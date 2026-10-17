package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// repairResultProps returns the properties that a mutation writes when it
// applies patch and parent to source. If patch assigns JSON null to a
// property, the mutation removes that property. If parent has a value, the
// mutation sets parent_id.
func repairResultProps(source, patch map[string]json.RawMessage, parent *node.ParentChange) map[string]json.RawMessage {
	result := maps.Clone(source)
	if result == nil {
		result = map[string]json.RawMessage{}
	}
	for name, value := range patch {
		if len(value) == 0 || string(value) == "null" {
			delete(result, name)
			continue
		}
		result[name] = value
	}
	if parent != nil {
		result[repairParentProperty] = json.RawMessage(strconv.Quote(parent.TargetID.String()))
	}
	return result
}

// validateRepairResult validates the complete resulting properties of one
// node of type nodeType with [node.Schema.ValidateResult] and the schema
// from a snapshot read. It reads the target of each reference value through
// a mutation snapshot. It returns the primary digest of each existing
// reference target. The mutation requires those records to remain
// unchanged. If the result is invalid, validateRepairResult returns a
// [node.PropertyResultError].
func validateRepairResult(ctx context.Context, mutator node.Mutator, entry NodeIntegrityEntry, schema node.SchemaSnapshot, nodeType string, props map[string]json.RawMessage) ([]node.GraphRecord, error) {
	interpreted := schema.Interpret(nodeType, props)
	references := make(map[uuid.UUID]node.ReferenceRecord, len(interpreted.References))
	records := []node.GraphRecord{}
	for _, reference := range interpreted.References {
		if _, read := references[reference.TargetID]; read {
			continue
		}
		snapshot, _, err := readNodeSnapshot(ctx, mutator, entry, schema.OrgID, reference.TargetID)
		if err != nil {
			return nil, err
		}
		references[reference.TargetID] = node.ReferenceRecord{Exists: snapshot.Exists, OrgID: snapshot.Node.OrgID, NodeType: snapshot.Node.NodeType}
		if snapshot.Exists {
			records = append(records, node.GraphRecord{NodeID: reference.TargetID, PrimaryDigest: snapshot.PrimaryDigest})
		}
	}
	result := schema.ValidateResult(nodeType, props, references)
	if err := result.Err(); err != nil {
		wrapped := fmt.Errorf("repair %s for node %s: validate the result: %w", entry.RepairID, entry.NodeID, err)
		slog.ErrorContext(ctx, "repair.result_invalid", slog.String("err", wrapped.Error()), slog.String("repair_id", entry.RepairID))
		return nil, wrapped
	}
	return records, nil
}

// mergeGraphRecords appends each record of extra to records unless records
// already lists the same node ID. It returns the merged records in node ID
// order.
func mergeGraphRecords(records, extra []node.GraphRecord) []node.GraphRecord {
	merged := append([]node.GraphRecord{}, records...)
	listed := make(map[uuid.UUID]bool, len(records))
	for _, record := range records {
		listed[record.NodeID] = true
	}
	for _, record := range extra {
		if !listed[record.NodeID] {
			listed[record.NodeID] = true
			merged = append(merged, record)
		}
	}
	sortGraphRecords(merged)
	return merged
}
