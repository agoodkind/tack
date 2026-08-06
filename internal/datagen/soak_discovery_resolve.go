package datagen

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type soakReferenceStrategy string
type soakDiscoveryNodeType string

const (
	soakReferenceUUIDOnly                soakReferenceStrategy = "uuid_only"
	soakReferenceDirectProperty          soakReferenceStrategy = "direct_property"
	soakReferenceScopedSequence          soakReferenceStrategy = "scoped_sequence"
	soakReferenceNestedScopedProperty    soakReferenceStrategy = "nested_scoped_property"
	soakReferenceWorkspaceScopedProperty soakReferenceStrategy = "workspace_scoped_property"
)

const (
	soakDiscoveryActivity  soakDiscoveryNodeType = "activity"
	soakDiscoveryComment   soakDiscoveryNodeType = "comment"
	soakDiscoveryCycle     soakDiscoveryNodeType = "cycle"
	soakDiscoveryEpic      soakDiscoveryNodeType = "epic"
	soakDiscoveryIssue     soakDiscoveryNodeType = "issue"
	soakDiscoveryLabel     soakDiscoveryNodeType = "label"
	soakDiscoveryModule    soakDiscoveryNodeType = "module"
	soakDiscoveryProject   soakDiscoveryNodeType = "project"
	soakDiscoveryState     soakDiscoveryNodeType = "state"
	soakDiscoveryWorkspace soakDiscoveryNodeType = "workspace"
)

type discoveryDefinition struct {
	token     string
	arguments ToolArguments
	reference string
}

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
	lookupReference := item.Reference
	switch strategy {
	case soakReferenceUUIDOnly:
		err := invalidCollectionReference(singular, item.Reference)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	case soakReferenceDirectProperty:
		if item.referenceIsName() {
			return recoverCollectionItem(ctx, driver, singular, item, recovery)
		}
	case soakReferenceScopedSequence:
		if !isResolvableSequenceReference(item.Reference, scopeReference) {
			return recoverCollectionItem(ctx, driver, singular, item, recovery)
		}
	case soakReferenceWorkspaceScopedProperty:
		return recoverCollectionItem(ctx, driver, singular, item, recovery)
	case soakReferenceNestedScopedProperty:
		if recovery != nil {
			return recoverCollectionItem(ctx, driver, singular, item, recovery)
		}
		referenceScope, _, scoped := strings.Cut(lookupReference, "::")
		if !scoped || !strings.EqualFold(referenceScope, scopeReference) {
			lookupReference = scopeReference + "::" + item.Name
		}
	}
	result, err := driver.Call(ctx, token, "tack_get_"+singular, ToolArguments{
		WorkspaceReference: workspaceReference,
		NodeID:             lookupReference,
	})
	if err != nil {
		wrapped := fmt.Errorf(
			"qa datagen soak: get %s %q: %w",
			singular,
			lookupReference,
			err,
		)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, wrapped)
	}
	resolved, err := resolvedCollectionNode(singular, item, result)
	if err != nil {
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	}
	return resolved, nil
}

func recoverCollectionItem(
	ctx context.Context,
	driver *Driver,
	singular string,
	item collectionItem,
	recovery *discoveryDefinition,
) (soakNode, error) {
	if recovery == nil {
		err := invalidCollectionReference(singular, item.Reference)
		return soakNode{}, loggedDiscoverySkip(ctx, singular, item, err)
	}
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

func soakNodeReferenceStrategy(singular string) (soakReferenceStrategy, error) {
	switch soakDiscoveryNodeType(singular) {
	case soakDiscoveryComment, soakDiscoveryActivity:
		return soakReferenceUUIDOnly, nil
	case soakDiscoveryWorkspace, soakDiscoveryProject:
		return soakReferenceDirectProperty, nil
	case soakDiscoveryIssue, soakDiscoveryEpic, soakDiscoveryCycle, soakDiscoveryModule:
		return soakReferenceScopedSequence, nil
	case soakDiscoveryLabel:
		return soakReferenceWorkspaceScopedProperty, nil
	case soakDiscoveryState:
		return soakReferenceNestedScopedProperty, nil
	default:
		return "", fmt.Errorf("qa datagen soak: unknown node type %q", singular)
	}
}

func isResolvableSequenceReference(reference, scopeReference string) bool {
	separator := strings.LastIndex(reference, "-")
	if separator <= 0 || separator == len(reference)-1 {
		return false
	}
	sequence, err := strconv.Atoi(reference[separator+1:])
	if err != nil || sequence <= 0 {
		return false
	}
	return strings.EqualFold(reference[:separator], scopeReference)
}

func invalidCollectionReference(singular, reference string) error {
	return fmt.Errorf(
		"qa datagen soak: %s list returned unresolvable reference %q",
		singular,
		reference,
	)
}
