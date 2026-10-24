package foundationdb

import (
	"bytes"
	"encoding/json"

	"goodkind.io/tack/internal/domain/node"
)

// mutationSearchChange returns searchChangeAccess when result preserves the
// stored source's name, every changed property has a definition with a
// search declaration that excludes the property from search text, and schema
// accepts each changed value in both the stored and the new properties. It
// returns searchChangeContent for a new node and in every other case. Only
// content work recomputes the property warnings of a node.
func mutationSearchChange(source mutationSource, result *node.Node, schema node.Schema) searchChange {
	if !source.Exists || source.Node.Name != result.Name {
		return searchChangeContent
	}
	excluded := excludedSearchProperties(schema.Definitions)
	changed := changedPropertyNames(source.Node.Props, result.Props)
	for _, name := range changed {
		if !excluded[name] {
			return searchChangeContent
		}
	}
	if rejectsProperty(schema, result.NodeType, changed, source.Node.Props) || rejectsProperty(schema, result.NodeType, changed, result.Props) {
		return searchChangeContent
	}
	return searchChangeAccess
}

// changedPropertyNames returns each property name that has a different value
// in result than in source, including each name that only one of them has.
func changedPropertyNames(source, result map[string]json.RawMessage) []string {
	changed := make([]string, 0, len(result))
	for name, value := range result {
		if !bytes.Equal(source[name], value) {
			changed = append(changed, name)
		}
	}
	for name := range source {
		if _, kept := result[name]; !kept {
			changed = append(changed, name)
		}
	}
	return changed
}

// rejectsProperty reports whether schema rejects the value in props of one of
// names for node type typeKey, or cannot interpret those values.
func rejectsProperty(schema node.Schema, typeKey string, names []string, props map[string]json.RawMessage) bool {
	values := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		if value, present := props[name]; present {
			values[name] = value
		}
	}
	if len(values) == 0 {
		return false
	}
	interpreted := schema.Interpret(typeKey, values)
	return len(interpreted.Invalid) > 0 || len(interpreted.Failures) > 0
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
