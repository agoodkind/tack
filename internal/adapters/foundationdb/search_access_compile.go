package foundationdb

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
)

// compileSearchAccess compiles every write version through the registered
// policies and merges the opaque keys. Search code never decodes a key.
func compileSearchAccess(
	ctx context.Context,
	policies *searchaccess.PolicySet,
	versions []string,
	orgID, nodeID uuid.UUID,
	generation int64,
) (node.SearchAccess, error) {
	if len(versions) == 0 {
		versions = []string{searchaccess.StableVersion}
	}
	merged := node.SearchAccess{Versions: slices.Clone(versions), Keys: []string{}, Generation: generation}
	for _, version := range versions {
		compiled, err := policies.Index(ctx, searchaccess.IndexAccessRequest{
			Version: version, OrganizationID: orgID, ResourceID: nodeID, Generation: generation,
		})
		if err != nil {
			return node.SearchAccess{}, nodeContentContextError{operation: fmt.Sprintf("compile search access version %s for node %s", version, nodeID), err: err}
		}
		merged.Keys = append(merged.Keys, compiled.Keys...)
	}
	slices.Sort(merged.Versions)
	merged.Versions = slices.Compact(merged.Versions)
	slices.Sort(merged.Keys)
	merged.Keys = slices.Compact(merged.Keys)
	return merged, nil
}
