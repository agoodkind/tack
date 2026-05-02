package tools

import (
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
)

// NodeTypeBinding carries the dependencies needed by the per-type CRUD tools.
type NodeTypeBinding struct {
	NodeSvc      *service.NodeService
	Reader       node.NodeReader
	PropertyDefs node.PropertyDefRepository
	Resolver     *Resolver
	// Users is optional. When set, response renderers resolve creator and
	// assignee UUIDs to display names. When nil, the renderer falls back to
	// raw IDs.
	Users user.Repository
}

// RegisterNodeTools registers list/create/get/update/delete tools for a NodeType.
func RegisterNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
		return
	}
	slug := strings.ToLower(nt.Slug)
	plural := strings.ToLower(nt.PluralSlug)
	if plural == "" {
		plural = slug + "s"
	}
	chain := b.Resolver.ScopeChainForType(nt)
	epParam := b.Resolver.EntryPointParamName()
	ops := opSet(nt.AllowedOps)

	if _, ok := ops[node.OpList]; ok {
		registerTool(s, listTool(nt, plural, chain, epParam, b.Resolver), listHandler(nt, chain, epParam, b))
	}
	if _, ok := ops[node.OpCreate]; ok {
		registerTool(s, createTool(nt, slug, chain, epParam, b.Resolver), createHandler(nt, chain, epParam, b))
	}
	if _, ok := ops[node.OpRead]; ok {
		registerTool(s, getTool(nt, slug, epParam, b.Resolver), getHandler(nt, b))
	}
	if _, ok := ops[node.OpUpdate]; ok {
		registerTool(s, updateTool(nt, slug, epParam, b.Resolver), updateHandler(nt, b))
	}
	if _, ok := ops[node.OpDelete]; ok {
		registerTool(s, deleteTool(nt, slug, epParam, b.Resolver), deleteHandler(nt, b))
	}
}

func opSet(ops []node.Op) map[node.Op]struct{} {
	m := make(map[node.Op]struct{}, len(ops))
	for _, op := range ops {
		m[op] = struct{}{}
	}
	return m
}
