package tools

import (
	"strings"

	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
)

// NodeTypeBinding supplies dependencies for the per-type create, read,
// update, delete, and list tools.
type NodeTypeBinding struct {
	NodeSvc      *service.NodeService
	Reader       node.NodeReader
	PropertyDefs node.PropertyDefRepository
	Resolver     *Resolver
	// Users optionally resolves creator and assignee UUIDs to display names.
	// Renderers use raw IDs when Users is nil.
	Users user.Repository
	// Relationships reads assigned_to relationships for nodes that the get and
	// list tools render. Every binding must set this field.
	Relationships node.RelationshipBatchLister
}

// RegisterNodeTools registers tools for one NodeType.
//
// AllowedOps selects the operations to register.
// FeatureExcludeFromGenericTools excludes the type from registration.
// The available operations are:
//
//   - List tools list the type's nodes.
//   - Create tools create nodes.
//   - Get tools read nodes.
//   - Update tools change nodes.
//   - Delete tools remove nodes.
func RegisterNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
		return
	}

	slug := strings.ToLower(nt.Slug)
	plural := strings.ToLower(nt.PluralSlug)
	if plural == "" {
		plural = slug + "s"
	}

	route := b.Resolver.ScopeRouteForType(nt)
	epParam := b.Resolver.EntryPointParamName()
	ops := opSet(nt.AllowedOps)

	if _, ok := ops[node.OpList]; ok {
		registerTool(s, listTool(nt, plural, route, epParam, b.Resolver), listHandler(nt, route, b))
	}

	if _, ok := ops[node.OpCreate]; ok {
		registerTool(
			s,
			createTool(nt, slug, route, epParam, b.Resolver),
			createHandler(nt, route, b),
		)
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
