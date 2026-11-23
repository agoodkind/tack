package tools

import "goodkind.io/tack/internal/domain/node"

func multiParentTypes() (
	*node.NodeType,
	*node.NodeType,
	*node.NodeType,
	*node.NodeType,
	*node.NodeType,
) {
	workspaceType := &node.NodeType{
		TypeKey:   "workspace",
		Slug:      "workspace",
		Name:      "Workspace",
		Features:  node.Features{node.FeatureIsEntryPoint, node.FeatureIsScope},
		Reference: node.ReferenceConfig{Strategy: node.ReferenceDirectProperty, Property: "slug"},
	}
	projectType := &node.NodeType{
		TypeKey:      "project",
		Slug:         "project",
		Name:         "Project",
		CanLiveUnder: []string{"workspace"},
		Features:     node.Features{node.FeatureIsScope},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
		},
	}
	issueType := &node.NodeType{
		TypeKey:      "issue",
		Slug:         "issue",
		Name:         "Issue",
		CanLiveUnder: []string{"project"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedSequence,
			Property: "sequence",
		},
		ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()},
	}
	epicType := &node.NodeType{
		TypeKey:      "epic",
		Slug:         "epic",
		Name:         "Epic",
		CanLiveUnder: []string{"project"},
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedSequence,
			Property: "sequence",
		},
		ReferenceTemplates: []node.ReferenceTemplate{sequenceTemplate()},
	}
	commentType := &node.NodeType{
		TypeKey:      "comment",
		Slug:         "comment",
		PluralSlug:   "comments",
		Name:         "Comment",
		CanLiveUnder: []string{"issue", "epic"},
		Reference:    node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly},
	}

	return workspaceType, projectType, issueType, epicType, commentType
}
