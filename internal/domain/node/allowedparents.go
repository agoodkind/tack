package node

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// parentReason returns the reason that the child_of targets in graph
// do not form a valid parent for a node of nodeType, or "" for a valid
// parent. It returns a read failure when Types does not contain the parent's
// node type.
func (s Schema) parentReason(
	nodeType *NodeType,
	graph ResultGraph,
) (string, *ReadFailure) {
	if len(graph.ChildOf) == 0 {
		if len(nodeType.CanLiveUnder) > 0 {
			return fmt.Sprintf(
				"node type %q requires a parent of type %v",
				nodeType.TypeKey,
				nodeType.CanLiveUnder,
			), nil
		}

		return "", nil
	}

	if len(graph.ChildOf) != 1 {
		return fmt.Sprintf("the node has more than one child_of edge: %v", graph.ChildOf), nil
	}

	parentID := graph.ChildOf[0]
	parent := graph.Targets[parentID]
	if !parent.Found || parent.OrgID != graph.OrgID {
		return fmt.Sprintf("the parent %s does not exist in the organization", parentID), nil
	}

	parentType := s.Types[parent.NodeType]
	if parentType == nil {
		return "", &ReadFailure{
			Subject: "node type " + parent.NodeType + " of parent " + parentID.String(),
			Reason:  "the schema does not contain it",
		}
	}

	if !LivesUnder(nodeType, parentType) {
		return fmt.Sprintf(
			"node type %q cannot live under the parent %s of type %q",
			nodeType.TypeKey,
			parentID,
			parentType.TypeKey,
		), nil
	}

	return "", nil
}

// resultReferenceID decodes one raw property value that stores a node ID as a
// JSON string. It returns [uuid.Nil] for any other value.
func resultReferenceID(raw json.RawMessage) uuid.UUID {
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
