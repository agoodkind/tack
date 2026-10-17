package ops

import (
	"fmt"
	"slices"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

// graphProblem returns the first graph validation error, or an empty string.
// It requires the node to exist with the declared type. For executable
// entries other than deletes, it checks the resulting parent chain and edge
// changes. A removed child_of edge to an existing node must target an
// ancestor above the resulting parent.
func graphProblem(entry NodeIntegrityEntry, evidence fdbadapter.RepairEvidence) string {
	if !evidence.Node.Found {
		return "the node does not exist"
	}
	if evidence.Node.NodeType != entry.NodeType {
		return fmt.Sprintf("the node has type %q, not %q", evidence.Node.NodeType, entry.NodeType)
	}
	if entry.Delete != nil || entry.Status != RepairEntryExecutable {
		return ""
	}
	if evidence.Cycle {
		return "the resulting parent chain contains a cycle"
	}
	present := relatedPresence(evidence)
	parentID := resultParent(entry.Result)
	if parentID != uuid.Nil && !present[parentID] {
		return fmt.Sprintf("the resulting parent %s does not exist in the organization", parentID)
	}
	outgoing := make(map[RepairEdge]bool, len(evidence.Outgoing))
	for _, record := range evidence.Outgoing {
		outgoing[RepairEdge{RelationType: record.RelationType, TargetID: record.TargetID}] = true
	}
	for _, edge := range entry.Result.RemoveEdges {
		if !outgoing[edge] {
			return fmt.Sprintf("the node has no %s edge to %s", edge.RelationType, edge.TargetID)
		}
		if edge.RelationType != node.RelChildOf || !present[edge.TargetID] {
			continue
		}
		if len(evidence.Chain) < 2 || !slices.Contains(evidence.Chain[1:], edge.TargetID) {
			return fmt.Sprintf("the removed child_of edge to %s does not target an ancestor above the resulting parent", edge.TargetID)
		}
	}
	for _, edge := range entry.Result.AddEdges {
		if edge.RelationType == node.RelChildOf {
			return "a result sets the parent through parent_id and cannot add a child_of edge"
		}
		if outgoing[edge] {
			return fmt.Sprintf("the node already has the %s edge to %s", edge.RelationType, edge.TargetID)
		}
		if !present[edge.TargetID] {
			return fmt.Sprintf("the added edge target %s does not exist in the organization", edge.TargetID)
		}
	}
	return ""
}

// withoutReportComment excludes an earlier phase's report comment from the
// evidence by removing its edges and related record. The frozen graph does
// not include that comment.
func withoutReportComment(evidence fdbadapter.RepairEvidence, commentID uuid.UUID) fdbadapter.RepairEvidence {
	if commentID == uuid.Nil {
		return evidence
	}
	evidence.Outgoing = slices.DeleteFunc(slices.Clone(evidence.Outgoing), func(edge fdbadapter.RepairEdgeRecord) bool {
		return edge.TargetID == commentID
	})
	evidence.Incoming = slices.DeleteFunc(slices.Clone(evidence.Incoming), func(edge fdbadapter.RepairEdgeRecord) bool {
		return edge.SourceID == commentID
	})
	evidence.Related = slices.DeleteFunc(slices.Clone(evidence.Related), func(record fdbadapter.RepairNodeRecord) bool {
		return record.NodeID == commentID
	})
	return evidence
}

func relatedPresence(evidence fdbadapter.RepairEvidence) map[uuid.UUID]bool {
	present := make(map[uuid.UUID]bool, len(evidence.Related))
	for _, record := range evidence.Related {
		present[record.NodeID] = record.Found && record.OrgID == evidence.Node.OrgID
	}
	return present
}
