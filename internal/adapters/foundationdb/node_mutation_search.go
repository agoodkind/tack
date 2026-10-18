package foundationdb

import (
	"bytes"

	"goodkind.io/tack/internal/domain/node"
)

// mutationSearchChange returns searchChangeAccess when result preserves the
// stored source's name and every changed property has a definition with a
// search declaration that excludes the property from search text. It returns
// searchChangeContent for a new node and in every other case.
func mutationSearchChange(source mutationSource, result *node.Node, definitions []*node.PropertyDef) searchChange {
	if !source.Exists || source.Node.Name != result.Name {
		return searchChangeContent
	}
	excluded := excludedSearchProperties(definitions)
	for name, value := range result.Props {
		if !bytes.Equal(source.Node.Props[name], value) && !excluded[name] {
			return searchChangeContent
		}
	}
	for name := range source.Node.Props {
		if _, kept := result.Props[name]; !kept && !excluded[name] {
			return searchChangeContent
		}
	}
	return searchChangeAccess
}

// excludedSearchProperties maps each property name to true when every
// definition with that name has a search declaration with Include set to
// false. If a definition lacks a search declaration or sets Include to true,
// excludedSearchProperties maps that definition's property name to false.
func excludedSearchProperties(definitions []*node.PropertyDef) map[string]bool {
	excluded := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition == nil {
			continue
		}
		declaredExcluded := definition.Search != nil && !definition.Search.Include
		previous, seen := excluded[definition.Name]
		excluded[definition.Name] = declaredExcluded && (!seen || previous)
	}
	return excluded
}
