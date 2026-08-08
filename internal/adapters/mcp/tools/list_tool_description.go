package tools

import (
	"fmt"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

func listToolDescription(nt *node.NodeType, plural string, route scopeRoute, epParam string, resolver *Resolver) string {
	slug := strings.ToLower(nt.Slug)
	if !route.hasLevels() {
		return fmt.Sprintf(
			"Lists %s in a workspace. Pass %s with the workspace reference from tack_list_workspaces. Optional filters apply exact property matches. Use this before tack_get_%s when you need all %s in that workspace.",
			plural,
			epParam,
			slug,
			plural,
		)
	}
	if len(route.Leaves) > 0 {
		requiredText := strings.Join(append([]string{epParam}, route.requiredParams()...), ", ")
		return fmt.Sprintf(
			"Lists %s in %s. Pass %s and exactly one of %s. Optional filters apply exact property matches.",
			plural,
			leafScopeText(route, resolver),
			requiredText,
			strings.Join(route.leafParamNames(), ", "),
		)
	}

	chain := route.Chain
	deepestScope := chain[len(chain)-1]
	scopeType := scopeTypeName(deepestScope, resolver)
	scopeText := scopeTypeNameText(scopeType, deepestScope)
	scopeHint := fmt.Sprintf("the %s reference", scopeText)
	if scopeExample := referenceExample(scopeReference(scopeType)); scopeExample != "" {
		scopeHint = fmt.Sprintf("%s, for example %s", scopeHint, scopeExample)
	}
	return fmt.Sprintf(
		"Lists %s in a %s. For a %s queue, pass %s and %s with %s. Optional filters apply exact property matches such as state or priority. Use this before tack_get_%s when you need all %s in that %s.",
		plural,
		scopeText,
		scopeText,
		epParam,
		deepestScope.ParamName,
		scopeHint,
		slug,
		plural,
		scopeText,
	)
}

func createToolDescription(nt *node.NodeType, route scopeRoute, epParam string, resolver *Resolver) string {
	typeName := strings.ToLower(nt.Name)
	if !route.hasLevels() {
		return fmt.Sprintf(
			"Creates %s %s in a workspace. Discover %s with tack_list_workspaces before calling this tool. Do not guess repo, org, or project names.",
			indefiniteArticle(typeName),
			typeName,
			epParam,
		)
	}
	if len(route.Leaves) > 0 {
		requiredText := strings.Join(append([]string{epParam}, route.requiredParams()...), ", ")
		return fmt.Sprintf(
			"Creates %s %s in %s. Pass %s and exactly one of %s. Discover %s with tack_list_workspaces before calling this tool. Do not guess repo, org, or project names.",
			indefiniteArticle(typeName),
			typeName,
			leafScopeText(route, resolver),
			requiredText,
			strings.Join(route.leafParamNames(), ", "),
			epParam,
		)
	}

	chain := route.Chain
	deepestScope := chain[len(chain)-1]
	scopeType := scopeTypeName(deepestScope, resolver)
	scopeText := scopeTypeNameText(scopeType, deepestScope)
	return fmt.Sprintf(
		"Creates %s %s in a %s. Discover %s with tack_list_workspaces and %s with tack_list_%s or tack_describe_%s before calling this tool. Do not guess repo, org, or project names.",
		indefiniteArticle(typeName),
		typeName,
		scopeText,
		epParam,
		deepestScope.ParamName,
		pluralSlug(scopeType, deepestScope),
		resolver.entryPointSlug,
	)
}

// leafScopeText names the alternative parents for tool descriptions, for
// example "an issue or epic".
func leafScopeText(route scopeRoute, resolver *Resolver) string {
	names := make([]string, 0, len(route.Leaves))
	for _, leaf := range route.Leaves {
		names = append(names, scopeTypeNameText(scopeTypeName(leaf.Level, resolver), leaf.Level))
	}
	joined := strings.Join(names, " or ")
	return indefiniteArticle(names[0]) + " " + joined
}

func indefiniteArticle(word string) string {
	if word == "" {
		return "a"
	}
	switch word[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "an"
	default:
		return "a"
	}
}

func entryPointFieldDescription(resolver *Resolver) string {
	return fmt.Sprintf("%s reference used as the MCP entry point.", resolver.entryPointSlug)
}

func scopeFieldDescription(level ScopeLevel, resolver *Resolver) string {
	scopeType := scopeTypeName(level, resolver)
	scopeText := scopeTypeNameText(scopeType, level)
	example := referenceExample(scopeReference(scopeType))
	if example == "" {
		return fmt.Sprintf("%s reference used to choose the scope.", scopeText)
	}
	return fmt.Sprintf("%s reference used to choose the scope, for example %s.", scopeText, example)
}

func scopeTypeName(level ScopeLevel, resolver *Resolver) *node.NodeType {
	if resolver == nil || resolver.typeIndex == nil {
		return nil
	}
	return resolver.typeIndex[level.TypeKey]
}

func scopeTypeNameText(nt *node.NodeType, level ScopeLevel) string {
	if nt != nil && nt.Slug != "" {
		return strings.ToLower(nt.Slug)
	}
	return level.Slug
}

func pluralSlug(nt *node.NodeType, level ScopeLevel) string {
	if nt != nil && nt.PluralSlug != "" {
		return strings.ToLower(nt.PluralSlug)
	}
	slug := scopeTypeNameText(nt, level)
	return slug + "s"
}

func scopeReference(nt *node.NodeType) node.ReferenceConfig {
	if nt == nil {
		return node.ReferenceConfig{}
	}
	return nt.Reference
}

func referenceExample(reference node.ReferenceConfig) string {
	switch reference.Strategy {
	case node.ReferenceDirectProperty:
		return "TACK"
	case node.ReferenceScopedSequence:
		return "TACK-65"
	case node.ReferenceScopedProperty:
		return "PROJECT::Name"
	default:
		return ""
	}
}
