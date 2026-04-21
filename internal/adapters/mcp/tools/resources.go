package tools

import (
	"context"
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterResources registers the MCP resources, all generated from the
// current NodeType set so that custom types automatically surface without
// string literal changes.
func RegisterResources(s *mcpserver.MCPServer, reader node.NodeReader, resolver *Resolver, nodeTypes []*node.NodeType) {
	s.AddResource(
		mcpmcp.Resource{
			URI:         "tack://onboard",
			Name:        "tack-onboard",
			Description: "Orientation guide for the Tack MCP server.",
			MIMEType:    "text/plain",
		},
		func(_ context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{URI: "tack://onboard", MIMEType: "text/plain", Text: buildOnboardText(resolver, nodeTypes)},
			}, nil
		},
	)
}

func buildOnboardText(resolver *Resolver, nodeTypes []*node.NodeType) string {
	var sb strings.Builder
	fmt.Fprintln(&sb, "Tack MCP: generic node-based project management.")
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Start by calling tack_list_workspaces to see available entry points.")
	fmt.Fprintf(&sb, "Then call tack_describe_%s with a %s to see node types, property defs, and direct children.\n", resolver.entryPointSlug, resolver.EntryPointParamName())
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Every entity is a node. CRUD tools are generated per NodeType:")
	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
			continue
		}
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		fmt.Fprintf(&sb, "  - tack_list_%s, tack_create_%s, tack_get_%s, tack_update_%s, tack_delete_%s\n", plural, nt.Slug, nt.Slug, nt.Slug, nt.Slug)
	}
	fmt.Fprintln(&sb)
	fmt.Fprintln(&sb, "Relationships between nodes (assigned_to, labeled_with, child_of, watches, etc.) are managed via tack_add_relationship / tack_remove_relationship / tack_list_relationships.")
	return sb.String()
}
