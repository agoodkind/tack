package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

const (
	// toolErrorAmbiguousReference matches the get error when a scoped reference
	// matches several nodes.
	toolErrorAmbiguousReference = "matched multiple nodes"
	// toolErrorNotFound matches the get error when a reference does not match
	// a node.
	toolErrorNotFound = ": not found"
)

// lookupScopedCollectionItem gets a node by its scoped name "<scope>::<name>".
// Recovery creates the node only when the get reports that the scope does
// not contain a node with that name. Discovery skips the item when the name
// matches several nodes.
func lookupScopedCollectionItem(
	ctx context.Context,
	driver *Driver,
	token string,
	workspaceReference string,
	singular string,
	item collectionItem,
	lookupReference string,
	recovery *discoveryDefinition,
) (soakNode, error) {
	result, err := driver.Call(ctx, token, "tack_get_"+singular, ToolArguments{
		WorkspaceReference: workspaceReference,
		NodeID:             lookupReference,
	})
	if err == nil {
		return matchedCollectionNode(ctx, singular, item, result)
	}
	if toolErrorContains(err, toolErrorAmbiguousReference) {
		wrapped := fmt.Errorf(
			"qa datagen soak: several %s nodes are named %q, so discovery does not choose one: %w",
			singular,
			lookupReference,
			err,
		)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, wrapped)
	}
	if recovery == nil || !toolErrorContains(err, toolErrorNotFound) {
		wrapped := fmt.Errorf("qa datagen soak: get %s %q: %w", singular, lookupReference, err)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, wrapped)
	}
	return recoverCollectionItem(ctx, driver, singular, item, recovery)
}

// scopedNameReference returns the listed reference when its scope matches
// the discovery scope. Otherwise, it joins the discovery scope and item name.
func scopedNameReference(item collectionItem, scopeReference string) string {
	referenceScope, _, scoped := strings.Cut(item.Reference, "::")
	if scoped && strings.EqualFold(referenceScope, scopeReference) {
		return item.Reference
	}
	return scopeReference + "::" + item.Name
}

func recoverCollectionItem(
	ctx context.Context,
	driver *Driver,
	singular string,
	item collectionItem,
	recovery *discoveryDefinition,
) (soakNode, error) {
	result, err := driver.Call(
		ctx, recovery.token, "tack_create_"+singular, recovery.arguments,
	)
	if err != nil {
		wrapped := fmt.Errorf(
			"qa datagen soak: recover %s %q: %w",
			singular,
			item.Name,
			err,
		)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, wrapped)
	}
	resolved, err := resolvedCollectionNode(singular, item, result)
	if err != nil {
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	}
	slog.InfoContext(
		ctx,
		"qa.datagen.soak.discovery_created",
		slog.String("node_type", singular),
		slog.String("name", item.Name),
		slog.String("raw_id", resolved.RawID),
	)
	return resolved, nil
}

func toolErrorContains(err error, text string) bool {
	var toolError *toolCallError
	return errors.As(err, &toolError) && strings.Contains(toolError.message, text)
}

func nameLookupUnsupported(singular string, item collectionItem) error {
	return fmt.Errorf(
		"qa datagen soak: %s list returned reference %q, and the MCP tools cannot look up a %s by name %q",
		singular,
		item.Reference,
		singular,
		item.Name,
	)
}
