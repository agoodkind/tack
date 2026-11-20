package datagen

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

type discoveryDefinition struct {
	token           string
	arguments       ToolArguments
	reference       string
	parentReference string
}

// resolveCollectionItem returns the raw ID of one listed node. A scoped
// property node, and a scoped sequence node without a resolvable sequence
// reference, is looked up by its scoped name "<scope>::<name>". Recovery
// creates the node only when that lookup finds no node with the name. A
// project listed by name is skipped, because the MCP tools cannot look up a
// project by name.
func resolveCollectionItem(
	ctx context.Context,
	driver *Driver,
	token string,
	workspaceReference string,
	singular string,
	item collectionItem,
	scopeReference string,
	recovery *discoveryDefinition,
) (soakNode, error) {
	if _, err := uuid.Parse(item.Reference); err == nil {
		return collectionItemNode(item, item.Reference, Result{}), nil
	}
	strategy, err := soakNodeReferenceStrategy(singular)
	if err != nil {
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	}
	switch strategy {
	case soakReferenceUUIDOnly:
		err := invalidCollectionReference(singular, item.Reference)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	case soakReferenceDirectProperty:
		if item.referenceIsName() {
			return soakNode{}, loggedDiscoverySkip(
				ctx,
				singular,
				item,
				nameLookupUnsupported(singular, item),
			)
		}
	case soakReferenceScopedSequence:
		if !isResolvableSequenceReference(item.Reference, scopeReference) {
			lookupReference := scopedNameReference(item, scopeReference)
			// The server prints "<scope>::<name>" only for a direct child of
			// the scope, and the get matches the name among direct children
			// only. A node listed under another reference can be a nested
			// node with the same name. Discovery creates a node only after a
			// lookup of the printed scoped name finds none.
			if lookupReference != item.Reference {
				recovery = nil
			}
			return lookupScopedCollectionItem(
				ctx,
				driver,
				token,
				workspaceReference,
				singular,
				item,
				lookupReference,
				recovery,
			)
		}
	case soakReferenceWorkspaceScopedProperty, soakReferenceNestedScopedProperty:
		lookupReference := scopedNameReference(item, scopeReference)
		return lookupScopedCollectionItem(
			ctx,
			driver,
			token,
			workspaceReference,
			singular,
			item,
			lookupReference,
			recovery,
		)
	}
	result, err := driver.Call(
		ctx,
		token,
		"tack_get_"+singular,
		ToolArguments{
			WorkspaceReference: workspaceReference,
			NodeID:             item.Reference,
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("qa datagen soak: get %s %q: %w", singular, item.Reference, err)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, wrapped)
	}
	return matchedCollectionNode(ctx, singular, item, result)
}

// matchedCollectionNode converts the get result of an existing node and logs
// the match.
func matchedCollectionNode(
	ctx context.Context,
	singular string,
	item collectionItem,
	result Result,
) (soakNode, error) {
	resolved, err := resolvedCollectionNode(singular, item, result)
	if err != nil {
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	}
	slog.InfoContext(
		ctx,
		"qa.datagen.soak.discovery_matched",
		slog.String("node_type", singular),
		slog.String("name", item.Name),
		slog.String("reference", item.Reference),
		slog.String("raw_id", resolved.RawID),
	)
	return resolved, nil
}

func resolvedCollectionNode(
	singular string,
	item collectionItem,
	result Result,
) (soakNode, error) {
	rawID := result.RawID()
	if _, err := uuid.Parse(rawID); err != nil {
		return soakNode{}, fmt.Errorf(
			"qa datagen soak: resolve %s %q returned invalid raw id %q",
			singular,
			item.Reference,
			rawID,
		)
	}
	return collectionItemNode(item, rawID, result), nil
}

func collectionItemNode(item collectionItem, rawID string, result Result) soakNode {
	return soakNode{
		Reference: rawID,
		RawID:     rawID,
		Name:      item.Name,
		Group:     stateGroup(result.field("Group")),
		SortOrder: result.intField("Sort order"),
	}
}
