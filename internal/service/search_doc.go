package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

// searchablePropertyTypes are the property types whose values are words a
// person would type into search. UUID, number, date, and checkbox values are
// excluded because they never match a keyword query.
var searchablePropertyTypes = map[node.PropertyType]bool{
	node.PropertyTypeText:        true,
	node.PropertyTypeSelect:      true,
	node.PropertyTypeMultiSelect: true,
	node.PropertyTypeURL:         true,
}

// SearchDocFromView builds the search document for a view, keeping only
// properties whose definition has a searchable type.
func SearchDocFromView(view *node.NodeView, defs []*node.PropertyDef) *domainsearch.NodeDoc {
	searchable := make(map[string]bool, len(defs))
	for _, def := range defs {
		if searchablePropertyTypes[def.Type] {
			searchable[def.Name] = true
		}
	}
	var props map[string]json.RawMessage
	for key, raw := range view.Props {
		if !searchable[key] {
			continue
		}
		if props == nil {
			props = make(map[string]json.RawMessage)
		}
		props[key] = raw
	}
	return &domainsearch.NodeDoc{
		ID:       view.ID.String(),
		OrgID:    view.OrgID.String(),
		NodeType: view.NodeType,
		Name:     view.Name,
		Props:    props,
	}
}

// indexSearchDoc writes the view's search document. Search indexing is
// best effort: a property definition read failure or an index failure is
// logged and the write that triggered it still succeeds.
func (s *NodeService) indexSearchDoc(ctx context.Context, log *slog.Logger, view *node.NodeView) {
	defs, err := s.propertyDefs.List(ctx, view.OrgID)
	if err != nil {
		log.WarnContext(ctx, "node.search_index: list property defs", slog.String("err", err.Error()))
		return
	}
	doc := SearchDocFromView(view, defs)
	if err := s.searcher.Index(ctx, "nodes", view.ID.String(), doc); err != nil {
		log.WarnContext(ctx, "node.search_index: index", slog.String("err", err.Error()))
	}
}
