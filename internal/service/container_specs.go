package service

import (
	"encoding/json"
	"strconv"

	"goodkind.io/tack/internal/domain/node"
)

func defaultContainerSpecs() []builtinTypeSpec {
	projectDefaultStates := []node.DefaultChild{
		defaultStateChild("Backlog", "backlog", "#9CA3AF", 0),
		defaultStateChild("Todo", "unstarted", "#6B7280", 1),
		defaultStateChild("In Progress", "started", "#3B82F6", defaultStartedStateRank),
		defaultStateChild("Done", "completed", "#10B981", defaultCompletedStateRank),
		defaultStateChild("Cancelled", "cancelled", "#EF4444", defaultCancelledStateRank),
	}
	workspaceFeatures := node.Features{
		node.FeatureHasAddress,
		node.FeatureIsContainer,
		node.FeatureIsEntryPoint,
		node.FeatureIsScope,
		node.FeatureExcludeFromGenericTools,
	}
	projectFeatures := node.Features{
		node.FeatureHasAddress,
		node.FeatureIsContainer,
		node.FeatureIsScope,
	}
	orgFeatures := node.Features{
		node.FeatureHasAddress,
		node.FeatureIsContainer,
		node.FeatureIsScope,
		node.FeatureExcludeFromGenericTools,
	}
	directIdentifier := node.ReferenceConfig{
		Strategy: node.ReferenceDirectProperty,
		Property: defaultIdentifierProperty,
	}
	slugAddressReference := node.ReferenceConfig{
		Strategy: node.ReferenceDirectProperty,
		Property: defaultSlugProperty,
	}

	return []builtinTypeSpec{
		{
			"org",
			"orgs",
			"Org",
			nodeTypeOrg,
			orgFeatures,
			slugAddressReference,
			[]string{nodeTypeWorkspace},
			nil,
			nil,
		},
		{
			"workspace",
			"workspaces",
			"Workspace",
			nodeTypeWorkspace,
			workspaceFeatures,
			slugAddressReference,
			[]string{nodeTypeProject, nodeTypeLabel, nodeTypeWorkspace},
			[]string{nodeTypeOrg},
			nil,
		},
		{
			"project",
			"projects",
			"Project",
			nodeTypeProject,
			projectFeatures,
			directIdentifier,
			[]string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule, nodeTypeState},
			[]string{nodeTypeWorkspace},
			projectDefaultStates,
		},
	}
}

func defaultStateChild(name string, group string, color string, sortOrder int) node.DefaultChild {
	groupRaw := mustRawString(group)
	colorRaw := mustRawString(color)
	sortRaw := json.RawMessage(strconv.Itoa(sortOrder))

	return node.DefaultChild{
		TypeKey: nodeTypeState,
		Name:    name,
		Props: map[string]json.RawMessage{
			defaultGroupProperty:     groupRaw,
			defaultColorProperty:     colorRaw,
			defaultSortOrderProperty: sortRaw,
		},
	}
}
