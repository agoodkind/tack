package service

import (
	"goodkind.io/tack/internal/domain/node"
)

func defaultSupportSpecs() []builtinNodeSpec {
	scopedName := node.ReferenceConfig{Strategy: node.ReferenceScopedProperty, Property: "name"}
	uuidOnly := node.ReferenceConfig{Strategy: node.ReferenceUUIDOnly, Property: ""}

	return []builtinNodeSpec{
		{
			"state",
			"states",
			"State",
			nodeTypeState,
			nil,
			scopedName,
			nil,
			[]string{nodeTypeProject},
			nil,
		},
		{
			"label",
			"labels",
			"Label",
			nodeTypeLabel,
			nil,
			scopedName,
			nil,
			[]string{nodeTypeWorkspace},
			nil,
		},
		{
			"comment",
			"comments",
			"Comment",
			nodeTypeComment,
			nil,
			uuidOnly,
			nil,
			[]string{nodeTypeIssue, nodeTypeEpic},
			nil,
		},
		{
			"activity",
			"activities",
			"Activity",
			nodeTypeActivity,
			nil,
			uuidOnly,
			nil,
			[]string{nodeTypeIssue, nodeTypeEpic, nodeTypeCycle, nodeTypeModule},
			nil,
		},
	}
}
