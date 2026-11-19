package datagen

import (
	"fmt"
	"strconv"
	"strings"
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
