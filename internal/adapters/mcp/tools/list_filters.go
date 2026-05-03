package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func normalizeListFilters(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	raw json.RawMessage,
) ([]node.PropertyMatch, error) {
	filters, err := parseProps(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid filters payload: %w", err)
	}
	filters, err = normalizeFilterAliases(ctx, b, nt, orgID, filters)
	if err != nil {
		return nil, err
	}
	filters, err = normalizePropertyCommandReferences(ctx, b, nt, orgID, scopeID, filters)
	if err != nil {
		return nil, err
	}
	matches := make([]node.PropertyMatch, 0, len(filters))
	keys := make([]string, 0, len(filters))
	for key := range filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		matches = append(matches, node.PropertyMatch{PropName: key, Value: filters[key]})
	}
	return matches, nil
}

func normalizeFilterAliases(
	ctx context.Context,
	b NodeTypeBinding,
	nt *node.NodeType,
	orgID uuid.UUID,
	filters map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	if filters == nil || b.PropertyDefs == nil {
		return filters, nil
	}
	defs, err := b.PropertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list property defs for filter aliases: %w", err)
	}
	normalized := make(map[string]json.RawMessage, len(filters))
	for name, raw := range filters {
		normalized[name] = raw
	}
	for _, def := range defs {
		if !propertyDefAppliesTo(def, nt) {
			continue
		}
		alias := referencePropertyAlias(def.Name)
		if alias == def.Name {
			continue
		}
		raw, hasAlias := normalized[alias]
		if !hasAlias {
			continue
		}
		if _, hasCanonical := normalized[def.Name]; hasCanonical {
			return nil, fmt.Errorf("filters contain both %q and %q: %w", alias, def.Name, domain.ErrInvalidArgument)
		}
		delete(normalized, alias)
		normalized[def.Name] = raw
	}
	return normalized, nil
}
