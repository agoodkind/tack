package datagen

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

// getToolPrefix precedes the node type key in each public get tool name.
const getToolPrefix = "tack_get_"

// ReferenceIdentities pairs titles with ReferenceResolver for unmatched results.
func ReferenceIdentities(titles *SearchTitles, driver *Driver, token, entry string) SearchIdentities {
	return SearchIdentities{Titles: titles, Resolve: ReferenceResolver(driver, token, entry)}
}

// ReferenceResolver calls the public get tool of the result's node type with
// token under entry. It supplies the printed reference as node_id and reads
// the raw node ID from the response.
func ReferenceResolver(driver *Driver, token, entry string) SearchResolver {
	return func(ctx context.Context, item SearchItem) (uuid.UUID, error) {
		if driver == nil {
			return uuid.Nil, loggedError(ctx, fmt.Sprintf("qa datagen: read reference %q without a driver", item.Title), errSearchResultUnresolved)
		}
		result, err := driver.Call(ctx, token, getToolPrefix+strings.ToLower(item.NodeType), ToolArguments{
			WorkspaceReference: entry, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
			NodeID: item.Title, Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "",
			TargetID: "", Limit: 0, Cursor: "",
		})
		if err != nil {
			return uuid.Nil, loggedError(ctx, fmt.Sprintf("qa datagen: read reference %q through its get tool", item.Title), err)
		}
		nodeID, err := uuid.Parse(result.RawID())
		if err != nil {
			return uuid.Nil, loggedError(ctx, fmt.Sprintf("qa datagen: parse the raw ID of reference %q", item.Title), err)
		}
		return nodeID, nil
	}
}

// storeResolver rejects results when orgID does not declare their node type.
// It uses byReference for types with a declared reference. For other types,
// it requires exactly one stored node in orgID with the result's type and
// printed name.
func storeResolver(stores *fdbadapter.Stores, orgID uuid.UUID, byReference SearchResolver) SearchResolver {
	return func(ctx context.Context, item SearchItem) (uuid.UUID, error) {
		types, err := stores.NodeTypes.List(ctx, orgID)
		if err != nil {
			return uuid.Nil, loggedError(ctx, "qa datagen: list node types of org "+orgID.String(), err)
		}
		var nodeType *node.NodeType
		for _, declared := range types {
			if declared.TypeKey == item.NodeType {
				nodeType = declared
			}
		}
		if nodeType == nil {
			return uuid.Nil, loggedError(ctx, fmt.Sprintf("qa datagen: org %s does not declare node type %q", orgID, item.NodeType), errSearchResultUnresolved)
		}
		if nodeType.Reference.Strategy != "" && nodeType.Reference.Strategy != node.ReferenceUUIDOnly {
			return byReference(ctx, item)
		}
		views, err := stores.Views.List(ctx, node.NodeListQuery{OrgID: orgID, NodeType: item.NodeType})
		if err != nil {
			return uuid.Nil, loggedError(ctx, "qa datagen: list nodes of type "+item.NodeType, err)
		}
		matches := make([]uuid.UUID, 0, 1)
		for _, view := range views {
			if view.Name == item.Title {
				matches = append(matches, view.ID)
			}
		}
		if len(matches) != 1 {
			return uuid.Nil, loggedError(ctx, fmt.Sprintf("qa datagen: org %s stores %d nodes of type %q named %q",
				orgID, len(matches), item.NodeType, item.Title), errSearchResultUnresolved)
		}
		return matches[0], nil
	}
}
