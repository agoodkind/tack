package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func canonicalizePropertyCommandKeys(
	nodeType *node.NodeType,
	defs []*node.PropertyDef,
	props map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	canonicalNames, aliasMap := allowedPropertyCommandKeys(nodeType, defs)
	compiled := make(map[string]json.RawMessage, len(props))

	seenNames := make(map[string]string, len(props))
	for name, raw := range props {
		canonicalName, err := canonicalPropertyCommandKey(name, canonicalNames, aliasMap)
		if err != nil {
			return nil, err
		}

		if seenName, exists := seenNames[canonicalName]; exists {
			err := fmt.Errorf(
				"properties contain both %q and %q: %w",
				seenName,
				name,
				domain.ErrInvalidArgument,
			)
			slog.Warn("node.property_alias_conflict", slog.String("err", err.Error()))

			return nil, err
		}

		seenNames[canonicalName] = name
		compiled[canonicalName] = raw
	}

	return compiled, nil
}

func allowedPropertyCommandKeys(
	nodeType *node.NodeType,
	defs []*node.PropertyDef,
) (map[string]struct{}, map[string]string) {
	canonicalNames := make(map[string]struct{}, len(defs))
	aliasMap := make(map[string]string)

	for _, def := range defs {
		if !node.PropertyAppliesTo(def, nodeType) {
			continue
		}

		canonicalNames[def.Name] = struct{}{}
		if def.ReferenceTargetTypeKey == "" || def.Type != node.PropertyTypeUUID {
			continue
		}

		alias := strings.TrimSuffix(def.Name, "_id")
		if alias == def.Name {
			continue
		}

		aliasMap[alias] = def.Name
	}

	canonicalNames[propertyParentID] = struct{}{}

	return canonicalNames, aliasMap
}

func canonicalPropertyCommandKey(
	name string,
	canonicalNames map[string]struct{},
	aliasMap map[string]string,
) (string, error) {
	if _, ok := canonicalNames[name]; ok {
		return name, nil
	}

	if canonicalName, ok := aliasMap[name]; ok {
		return canonicalName, nil
	}

	err := fmt.Errorf(
		"properties.%s is not declared for this node type: %w",
		name,
		domain.ErrInvalidArgument,
	)
	slog.Warn("node.property_undeclared", slog.String("err", err.Error()))

	return "", err
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}

	return value, true
}
