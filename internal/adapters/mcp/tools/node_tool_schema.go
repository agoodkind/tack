package tools

import (
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/domain/node"
)

func listTool(nt *node.NodeType, plural string, route scopeRoute, epParam string, resolver *Resolver) mcpmcp.Tool {
	fields := append([]schemaField{}, entryPointSchemaFields(resolver)...)
	fields = append(fields, schemaField{Name: "filters", Type: schemaObject, Desc: "Optional exact property filters keyed by property name or reference alias, for example {\"state\": \"TACK::In Progress\"} or {\"priority\": \"high\"}."})
	fields = append(fields, route.schemaFields(resolver)...)
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", plural),
		Description: listToolDescription(nt, plural, route, epParam, resolver),
		InputSchema: schema{Fields: fields, Required: append([]string{epParam}, route.requiredParams()...)}.toMCP(),
	}
}

func createTool(nt *node.NodeType, slug string, route scopeRoute, epParam string, resolver *Resolver) mcpmcp.Tool {
	fields := append([]schemaField{}, entryPointSchemaFields(resolver)...)
	fields = append(fields,
		schemaField{Name: "name", Type: schemaString, Desc: fmt.Sprintf("Name for the new %s.", strings.ToLower(nt.Name))},
		schemaField{Name: "properties", Type: schemaObject, Desc: "Property values keyed by name"},
	)
	fields = append(fields, route.schemaFields(resolver)...)
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_create_%s", slug),
		Description: createToolDescription(nt, route, epParam, resolver),
		InputSchema: schema{Fields: fields, Required: append([]string{epParam, "name"}, route.requiredParams()...)}.toMCP(),
	}
}

// nodeIDSchema is the get, update, and delete input. The entry point
// reference is required alongside node_id so the handler always checks that
// the node lies under a workspace the caller can name.
func nodeIDSchema(entryPointParam string, resolver *Resolver) schema {
	fields := []schemaField{{Name: "node_id", Type: schemaString}}
	required := []string{"node_id"}
	if entryPointParam != "" {
		fields = append(fields, entryPointSchemaFields(resolver)...)
		required = append(required, entryPointParam)
	}
	return schema{Fields: fields, Required: required}
}

func getTool(nt *node.NodeType, slug string, entryPointParam string, resolver *Resolver) mcpmcp.Tool {
	description := fmt.Sprintf("Gets a %s by UUID.", nt.Name)
	switch nt.Reference.Strategy {
	case node.ReferenceScopedSequence:
		description = fmt.Sprintf("Gets a %s by UUID or sequence reference like TACK-65.", nt.Name)
	case node.ReferenceScopedProperty:
		description = fmt.Sprintf("Gets a %s by UUID or scoped reference like PROJECT::Name.", nt.Name)
	case node.ReferenceDirectProperty:
		description = fmt.Sprintf("Gets a %s by UUID or declared reference.", nt.Name)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_get_%s", slug),
		Description: description,
		InputSchema: nodeIDSchema(entryPointParam, resolver).toMCP(),
	}
}

func updateTool(nt *node.NodeType, slug string, entryPointParam string, resolver *Resolver) mcpmcp.Tool {
	base := nodeIDSchema(entryPointParam, resolver)
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_update_%s", slug),
		Description: fmt.Sprintf("Updates a %s. Only provided fields change.", nt.Name),
		InputSchema: schema{
			Fields: append(base.Fields,
				schemaField{Name: "name", Type: schemaString},
				schemaField{Name: "properties", Type: schemaObject},
			),
			Required: base.Required,
		}.toMCP(),
	}
}

func deleteTool(nt *node.NodeType, slug string, entryPointParam string, resolver *Resolver) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_delete_%s", slug),
		Description: fmt.Sprintf("Deletes a %s.", nt.Name),
		InputSchema: nodeIDSchema(entryPointParam, resolver).toMCP(),
	}
}
