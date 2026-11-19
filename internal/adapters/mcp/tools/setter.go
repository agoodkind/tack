package tools

import (
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/domain/node"
)

func referencePropertyAppliesToTool(
	def *node.PropertyDef,
	nodeType *node.NodeType,
	typeIndex map[string]*node.NodeType,
) bool {
	if def.Type != node.PropertyTypeUUID {
		return false
	}

	if def.ReferenceTargetTypeKey == "" {
		return false
	}

	if typeIndex[def.ReferenceTargetTypeKey] == nil {
		return false
	}

	return node.PropertyAppliesTo(def, nodeType)
}

func referencePropertyAlias(name string) string {
	return strings.TrimSuffix(name, "_id")
}

func referencePropertyTool(
	nodeType *node.NodeType,
	def *node.PropertyDef,
	alias string,
	binding NodeTypeBinding,
) mcpmcp.Tool {
	epParam := binding.Resolver.EntryPointParamName()
	chain := binding.Resolver.ScopeChainForType(nodeType)
	nodeParam := typeReferenceParamName(nodeType.Slug)
	targetType := binding.Resolver.typeIndex[def.ReferenceTargetTypeKey]
	fields := append(
		entryPointSchemaFields(binding.Resolver),
		schemaField{
			Name: nodeParam,
			Type: schemaString,
			Desc: fmt.Sprintf(
				"UUID or printed reference for the %s to update.",
				strings.ToLower(nodeType.Name),
			),
			Enum: nil,
		},
		schemaField{
			Name: alias,
			Type: schemaString,
			Desc: referenceSetterValueDescription(alias, targetType),
			Enum: nil,
		},
	)
	required := make([]string, 0, len(chain)+len([]string{epParam, nodeParam, alias}))
	required = append(required, epParam, nodeParam, alias)

	for _, level := range chain {
		fields = append(fields, scopeReferenceFields(level, binding.Resolver)...)
		required = append(required, level.ParamName)
	}

	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_set_%s_%s", strings.ToLower(nodeType.Slug), alias),
		Description: referenceSetterDescription(nodeType, def, alias, targetType),
		InputSchema: schema{Fields: fields, Required: required}.toMCP(),
	}
}

func referenceSetterDescription(
	nodeType *node.NodeType,
	def *node.PropertyDef,
	alias string,
	targetType *node.NodeType,
) string {
	targetPlural := pluralSlug(
		targetType,
		ScopeLevel{TypeKey: "", Slug: def.ReferenceTargetTypeKey, ParamName: ""},
	)
	typeName := strings.ToLower(nodeType.Name)

	return fmt.Sprintf(
		"Sets %s on %s %s using UUID or printed reference input. "+
			"Discover valid %s values with tack_list_%s before calling this tool.",
		def.Name,
		indefiniteArticle(typeName),
		typeName,
		alias,
		targetPlural,
	)
}

func referenceSetterValueDescription(alias string, targetType *node.NodeType) string {
	targetText := scopeTypeNameText(targetType, ScopeLevel{TypeKey: "", Slug: alias, ParamName: ""})
	targetPlural := pluralSlug(targetType, ScopeLevel{TypeKey: "", Slug: alias, ParamName: ""})

	return fmt.Sprintf(
		"UUID or printed reference for the %s. Use tack_list_%s to discover valid values.",
		targetText,
		targetPlural,
	)
}
