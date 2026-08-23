package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

// RegisterSearch registers tack_search.
func RegisterSearch(s *mcpserver.MCPServer, searcher domainsearch.Searcher, resolver *Resolver) {
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_search",
			Description: "Full-text search across all nodes in a workspace's org. Filters are raw (field=value) equality.",
			InputSchema: schema{
				Fields: append(append(entryPointSchemaFields(resolver),
					schemaField{Name: "query", Type: schemaString},
					schemaField{Name: "node_type", Type: schemaString},
				), searchScopeFields(resolver)...),
				Required: []string{resolver.EntryPointParamName(), "query"},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			entryPointReference, ok := resolver.entryPointReference(args)
			if !ok {
				return recoverableError(resolver.entryPointRequiredMessage()), nil
			}
			query, ok := requireString(args, "query")
			if !ok {
				return recoverableError("query is required"), nil
			}
			nodeTypeFilter := optionalString(args, "node_type")

			ws, err := resolver.Workspace(ctx, entryPointReference)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			scope, err := searchScope(ctx, resolver, args, ws)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			if exact := exactSearchResult(ctx, resolver, nodeTypeFilter, query, scope); len(exact) > 0 {
				rc := newRenderCtxWithTypes(ctx, resolver.reader, nil, resolver.typeIndex)
				return successText(renderSearchViews(rc, exact), ""), nil
			}
			filters := map[string]string{"org_id": ws.OrgID.String()}
			if nodeTypeFilter != "" {
				filters["node_type"] = nodeTypeFilter
			}
			docs, _, err := searcher.Search(ctx, "nodes", query, filters)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			views := fullTextSearchViews(ctx, resolver, ws, scope, docs)
			rc := newRenderCtxWithTypes(ctx, resolver.reader, nil, resolver.typeIndex)
			return successText(renderSearchViews(rc, views), ""), nil
		},
	)
}

// fullTextSearchViews fetches every indexed hit and keeps only nodes of the
// workspace's own org that lie under the requested scope. The index is not
// the authority on org isolation: a stale or corrupted document must not
// leak another org's node.
func fullTextSearchViews(ctx context.Context, resolver *Resolver, ws *node.NodeView, scope *node.NodeView, docs []domainsearch.NodeDoc) []*node.NodeView {
	views := make([]*node.NodeView, 0, len(docs))
	for _, d := range docs {
		id, err := uuid.Parse(d.ID)
		if err != nil {
			continue
		}
		view, err := resolver.reader.Get(ctx, id)
		if err != nil || view == nil {
			continue
		}
		if view.OrgID != ws.OrgID {
			continue
		}
		if scope != nil && !viewBelongsToScope(ctx, resolver, view, scope.ID) {
			continue
		}
		views = append(views, view)
	}
	return views
}

func searchScopeFields(resolver *Resolver) []schemaField {
	fields := make([]schemaField, 0, len(resolver.scopeChain))
	for _, level := range resolver.scopeChain {
		fields = append(fields, scopeReferenceFields(level, resolver)...)
	}
	return fields
}

func searchScope(ctx context.Context, resolver *Resolver, args argMap, workspace *node.NodeView) (*node.NodeView, error) {
	parent := workspace
	for _, level := range resolver.scopeChain {
		value := optionalScopeReference(args, level)
		if value == "" {
			continue
		}
		next, err := resolver.ResolveScope(ctx, parent, level, value)
		if err != nil {
			return nil, err
		}
		parent = next
	}
	if parent.ID == workspace.ID {
		return nil, nil
	}
	return parent, nil
}

func exactSearchResult(ctx context.Context, resolver *Resolver, nodeTypeFilter, query string, scope *node.NodeView) []*node.NodeView {
	id, err := resolveExactSearchID(ctx, resolver, nodeTypeFilter, query)
	if err != nil {
		return nil
	}
	view, err := resolver.reader.Get(ctx, id)
	if err != nil || view == nil {
		return nil
	}
	if scope != nil && !viewBelongsToScope(ctx, resolver, view, scope.ID) {
		return nil
	}
	return []*node.NodeView{view}
}

func resolveExactSearchID(ctx context.Context, resolver *Resolver, nodeTypeFilter, query string) (uuid.UUID, error) {
	if nodeTypeFilter == "" {
		return resolver.ResolveNodeID(ctx, query)
	}
	nodeType := resolver.typeIndex[nodeTypeFilter]
	if nodeType == nil {
		return uuid.Nil, fmt.Errorf("node type %q not found", nodeTypeFilter)
	}
	return resolver.ResolveTypedNodeID(ctx, nodeType, query)
}
