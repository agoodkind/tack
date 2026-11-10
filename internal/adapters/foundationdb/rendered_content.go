package foundationdb

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func renderContentSnapshot(
	ctx context.Context,
	nodeID uuid.UUID,
	snapshot nodeContentSnapshot,
) (node.SearchRender, error) {
	render, err := node.RenderSearchText(
		ctx,
		node.SearchSource{
			OrgID:       snapshot.node.OrgID,
			NodeID:      nodeID,
			NodeType:    snapshot.nodeType,
			Name:        snapshot.node.Name,
			Properties:  snapshot.node.Props,
			Definitions: snapshot.definitions,
		},
	)
	if err != nil {
		return render, nodeContentContextError{
			operation: "render search text for node " + nodeID.String(),
			err:       err,
		}
	}

	return render, nil
}
