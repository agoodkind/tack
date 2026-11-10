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

// ReferenceIdentities pairs titles with [ReferenceResolver] for unmatched
// results.
//
// The returned value uses the supplied registry without adding resolved results
// to it. Its resolver uses driver, token, and entry for public reads.
//
//	identities := ReferenceIdentities(NewSearchTitles(), driver, token, entry)
//	nodeIDs, err := identities.IDs(ctx, items)
func ReferenceIdentities(titles *SearchTitles, driver *Driver, token, entry string) SearchIdentities {
	return SearchIdentities{Titles: titles, Resolve: ReferenceResolver(driver, token, entry)}
}

// ReferenceResolver calls the public get tool for the result's node type with
// token under entry.
//
// The resolver supplies the printed reference as node_id and reads the raw node
// ID from the response.
//
//   - The tool name uses the lowercased node type after tack_get_.
//   - A missing driver or invalid returned UUID produces an error.
func ReferenceResolver(driver *Driver, token, entry string) SearchResolver {
	return func(ctx context.Context, item SearchItem) (uuid.UUID, error) {
		if driver == nil {
			return uuid.Nil, loggedError(
				ctx,
				fmt.Sprintf("qa datagen: read reference %q without a driver", item.Title),
				errSearchResultUnresolved,
			)
		}
		result, err := driver.Call(
			ctx,
			token,
			getToolPrefix+strings.ToLower(item.NodeType),
			ToolArguments{
				WorkspaceReference: entry,
				ProjectReference:   "",
				IssueReference:     "",
				Name:               "",
				Properties:         nil,
				NodeID:             item.Title,
				Query:              "",
				NodeType:           "",
				Direction:          "",
				SourceID:           "",
				RelationType:       "",
				TargetID:           "",
				Limit:              0,
				Cursor:             "",
			},
		)
		if err != nil {
			return uuid.Nil, loggedError(
				ctx,
				fmt.Sprintf("qa datagen: read reference %q through its get tool", item.Title),
				err,
			)
		}
		nodeID, err := uuid.Parse(result.RawID())
		if err != nil {
			return uuid.Nil, loggedError(
				ctx,
				fmt.Sprintf("qa datagen: parse the raw ID of reference %q", item.Title),
				err,
			)
		}
		return nodeID, nil
	}
}

// storeResolver lists node types in orgID on its first successful load and
// rejects results with undeclared node types. A failed load can retry on the
// next resolver call.
//
// Types with a declared reference use byReference, except for UUID-only
// references. Other types require exactly one stored node in orgID with the
// result's type and printed name.
func storeResolver(stores *fdbadapter.Stores, orgID uuid.UUID, byReference SearchResolver) SearchResolver {
	var declared map[string]*node.NodeType
	return func(ctx context.Context, item SearchItem) (uuid.UUID, error) {
		if declared == nil {
			types, err := stores.NodeTypes.List(ctx, orgID)
			if err != nil {
				return uuid.Nil, loggedError(
					ctx,
					"qa datagen: list node types of org "+orgID.String(),
					err,
				)
			}
			declared = make(map[string]*node.NodeType, len(types))
			for _, nodeType := range types {
				declared[nodeType.TypeKey] = nodeType
			}
		}
		nodeType, found := declared[item.NodeType]
		if !found {
			return uuid.Nil, loggedError(
				ctx,
				fmt.Sprintf(
					"qa datagen: org %s does not declare node type %q",
					orgID,
					item.NodeType,
				),
				errSearchResultUnresolved,
			)
		}
		if nodeType.Reference.Strategy != "" && nodeType.Reference.Strategy != node.ReferenceUUIDOnly {
			return byReference(ctx, item)
		}
		views, err := stores.Views.List(
			ctx,
			node.NodeListQuery{OrgID: orgID, NodeType: item.NodeType},
		)
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
			return uuid.Nil, loggedError(
				ctx,
				fmt.Sprintf(
					"qa datagen: org %s stores %d nodes of type %q named %q",
					orgID,
					len(matches),
					item.NodeType,
					item.Title,
				),
				errSearchResultUnresolved,
			)
		}
		return matches[0], nil
	}
}
