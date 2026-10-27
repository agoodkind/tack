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
// node of type nodeType using node.Schema.ValidateResult and the schema from
// a snapshot read. childOf lists the targets of the child_of edges that the
// node has after the mutation.
//
// It reads the parent_id target and each reference target through a mutation
// snapshot and passes them to ValidateResult in a node.ResultGraph.
//
// It returns the primary digest of each existing target. The mutation
// requires those records to remain unchanged. If the result is invalid,
// validateRepairResult returns a node.PropertyResultError.
func validateRepairResult(ctx context.Context, mutator node.Mutator, entry NodeIntegrityEntry, schema node.SchemaSnapshot, nodeType string, props map[string]json.RawMessage, childOf []uuid.UUID) ([]node.GraphRecord, error) {
	graph := node.ResultGraph{OrgID: schema.OrgID, ChildOf: childOf, Targets: map[uuid.UUID]node.ResultTarget{}}
	records := []node.GraphRecord{}
	for _, targetID := range resultTargetIDs(schema, nodeType, props) {
		if _, read := graph.Targets[targetID]; read {
			continue
		}
		snapshot, _, err := readNodeSnapshot(ctx, mutator, entry, schema.OrgID, targetID)
		if err != nil {
			return nil, err
		}
		graph.Targets[targetID] = node.ResultTarget{Found: snapshot.Exists, OrgID: snapshot.Node.OrgID, NodeType: snapshot.Node.NodeType}
		if snapshot.Exists {
			records = append(records, node.GraphRecord{NodeID: targetID, PrimaryDigest: snapshot.PrimaryDigest})
		}
	}
	result := schema.ValidateResult(nodeType, props, graph)
	if err := result.Err(); err != nil {
		wrapped := fmt.Errorf("repair %s for node %s: validate the result: %w", entry.RepairID, entry.NodeID, err)
		slog.ErrorContext(ctx, "repair.result_invalid", slog.String("err", wrapped.Error()), slog.String("repair_id", entry.RepairID))
		return nil, wrapped
	}
	return records, nil
}

// resultChildOf returns the child_of targets of the node after a repair.
// When parent has a value, the mutation leaves one child_of edge to that
// parent. Otherwise, resultChildOf returns the child_of targets in snapshot.
func resultChildOf(snapshot node.MutationSnapshot, parent *node.ParentChange) []uuid.UUID {
	if parent != nil {
		return []uuid.UUID{parent.TargetID}
	}
	targets := []uuid.UUID{}
	for _, edge := range snapshot.Outgoing {
		if edge.Relationship.RelationType == node.RelChildOf {
			targets = append(targets, edge.Relationship.TargetID)
		}
	}
	return targets
}

// resultTargetIDs returns the node ID in parent_id and the node ID in each
// applicable UUID property that declares a reference target type. It skips
// any value that does not represent a node ID.
func resultTargetIDs(schema node.SchemaSnapshot, nodeType string, props map[string]json.RawMessage) []uuid.UUID {
	targets := []uuid.UUID{}
	if parentID := repairNodeID(props[repairParentProperty]); parentID != uuid.Nil {
		targets = append(targets, parentID)
	}
	kind := schema.Types[nodeType]
	if kind == nil {
		return targets
	}
	for _, definition := range schema.Definitions {
		if definition == nil || definition.Type != node.PropertyTypeUUID || definition.ReferenceTargetTypeKey == "" || !node.PropertyAppliesTo(definition, kind) {
			continue
		}
		if targetID := repairNodeID(props[definition.Name]); targetID != uuid.Nil {
			targets = append(targets, targetID)
		}
	}
	return targets
}

// repairNodeID decodes and returns a node ID from a JSON string. It returns
// [uuid.Nil] for any other value.
func repairNodeID(raw json.RawMessage) uuid.UUID {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

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
