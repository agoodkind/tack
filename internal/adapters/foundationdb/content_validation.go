package foundationdb

import (
	"slices"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func validateContentSnapshot(
	request searchdomain.ContentRequest,
	snapshot nodeContentSnapshot,
) error {
	if snapshot.revision <= 0 ||
		request.ProjectionConfig != "" && request.ProjectionConfig != snapshot.projectionID {
		return nodeContentContextError{
			operation: "validate revision and projection for node " + request.NodeID.String(),
			err:       node.ErrContentChanged,
		}
	}

	if request.SearchGeneration > 0 && request.SearchGeneration != snapshot.generation {
		return searchdomain.ErrWorkChanged
	}

	if len(request.AccessVersions) > 0 &&
		!slices.Equal(request.AccessVersions, snapshot.access.Versions) {
		return searchdomain.ErrWorkChanged
	}

	return nil
}
