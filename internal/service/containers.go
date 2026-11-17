package service

import (
	"encoding/json"
	"strconv"

	"goodkind.io/tack/internal/domain/node"
)

func defaultContainerSpecs() []builtinNodeSpec {
	stateChild := func(name, group, color string, sortOrder int) node.DefaultChild {
		groupRaw := mustRawString(group)
		colorRaw := mustRawString(color)
		sortRaw := json.RawMessage(strconv.Itoa(sortOrder))

		return node.DefaultChild{
			TypeKey: nodeTypeState,
			Name:    name,
			Props: map[string]json.RawMessage{
				propertyGroup:     groupRaw,
				propertyColor:     colorRaw,
				propertySortOrder: sortRaw,
			},
		}
	}
	projectDefaultStates := []node.DefaultChild{
		stateChild("Backlog", "backlog", "#9CA3AF", 0),
		stateChild("Todo", "unstarted", "#6B7280", 1),
		stateChild("In Progress", "started", "#3B82F6", stateInProgressOrder),
		stateChild("Done", "completed", "#10B981", stateDoneOrder),
		stateChild("Cancelled", "cancelled", "#EF4444", stateCancelledOrder),
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
		Property: propertyIdentifier,
	}
	slugAddressReference := node.ReferenceConfig{
		Strategy: node.ReferenceDirectProperty,
		Property: propertySlug,
	}

	return []builtinNodeSpec{
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
