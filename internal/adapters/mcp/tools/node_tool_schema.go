package tools

import (
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	"goodkind.io/tack/internal/domain/node"
)

func listTool(nt *node.NodeType, plural string, chain []ScopeLevel, epParam string, resolver *Resolver) mcpmcp.Tool {
	fields := append([]schemaField{}, entryPointSchemaFields(resolver)...)
	fields = append(fields, schemaField{Name: "filters", Type: schemaObject, Desc: "Optional exact property filters keyed by property name or reference alias, for example {\"state\": \"TACK::In Progress\"} or {\"priority\": \"high\"}."})
	for _, level := range chain {
		fields = append(fields, scopeReferenceFields(level, resolver)...)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", plural),
		Description: listToolDescription(nt, plural, chain, epParam, resolver),
		InputSchema: schema{Fields: fields, Required: append([]string{epParam}, scopeParamNames(chain)...)}.toMCP(),
	}
}

func createTool(nt *node.NodeType, slug string, chain []ScopeLevel, epParam string, resolver *Resolver) mcpmcp.Tool {
	fields := append([]schemaField{}, entryPointSchemaFields(resolver)...)
	fields = append(fields,
		schemaField{Name: "name", Type: schemaString, Desc: fmt.Sprintf("Name for the new %s.", strings.ToLower(nt.Name))},
		schemaField{Name: "properties", Type: schemaObject, Desc: "Property values keyed by name"},
	)
	for _, level := range chain {
		fields = append(fields, scopeReferenceFields(level, resolver)...)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_create_%s", slug),
		Description: createToolDescription(nt, chain, epParam, resolver),
		InputSchema: schema{Fields: fields, Required: append([]string{epParam, "name"}, scopeParamNames(chain)...)}.toMCP(),
	}
}

func nodeIDSchema(entryPointParam string, resolver *Resolver) schema {
	fields := []schemaField{{Name: "node_id", Type: schemaString}}
	if entryPointParam != "" {
		fields = append(fields, entryPointSchemaFields(resolver)...)
	}
	return schema{Fields: fields, Required: []string{"node_id"}}
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
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_update_%s", slug),
		Description: fmt.Sprintf("Updates a %s. Only provided fields change.", nt.Name),
		InputSchema: schema{
			Fields: append(nodeIDSchema(entryPointParam, resolver).Fields,
				schemaField{Name: "name", Type: schemaString},
				schemaField{Name: "properties", Type: schemaObject},
			),
			Required: []string{"node_id"},
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
