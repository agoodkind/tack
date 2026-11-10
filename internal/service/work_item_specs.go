package service

import "goodkind.io/tack/internal/domain/node"

func defaultWorkItemSpecs() []builtinTypeSpec {
	issueFeatures := node.Features{
		node.FeatureHasSequenceID,
		node.FeatureHasWorkflowStates,
		node.FeatureHasAssignees,
		node.FeatureHasActivity,
		node.FeatureHasComments,
		node.FeatureHasDueDates,
	}
	epicFeatures := append(node.Features{node.FeatureIsContainer}, issueFeatures...)
	cycleFeatures := node.Features{
		node.FeatureHasDueDates,
		node.FeatureHasActivity,
		node.FeatureIsContainer,
	}
	moduleFeatures := node.Features{node.FeatureHasActivity, node.FeatureIsContainer}
	scopedSequence := node.ReferenceConfig{
		Strategy: node.ReferenceScopedSequence,
		Property: defaultSequenceProperty,
	}

	return []builtinTypeSpec{
		{
			"issue",
			"issues",
			"Issue",
			nodeTypeIssue,
			issueFeatures,
			scopedSequence,
			[]string{nodeTypeComment, nodeTypeActivity},
			[]string{nodeTypeProject, nodeTypeEpic, nodeTypeCycle, nodeTypeModule, nodeTypeIssue},
			nil,
		},
		{
			"epic",
			"epics",
			"Epic",
			nodeTypeEpic,
			epicFeatures,
			scopedSequence,
			[]string{nodeTypeIssue, nodeTypeComment, nodeTypeActivity},
			[]string{nodeTypeProject, nodeTypeEpic},
			nil,
		},
		{
			"cycle",
			"cycles",
			"Cycle",
			nodeTypeCycle,
			cycleFeatures,
			scopedSequence,
			[]string{nodeTypeIssue},
			[]string{nodeTypeProject},
			nil,
		},
		{
			"module",
			"modules",
			"Module",
			nodeTypeModule,
			moduleFeatures,
			scopedSequence,
			[]string{nodeTypeIssue},
			[]string{nodeTypeProject},
			nil,
		},
	}
}
