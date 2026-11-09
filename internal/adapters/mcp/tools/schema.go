// Package tools builds MCP tools with typed schemas and response structures.
// The pinned mcp-go API declares ToolArgumentsSchema.Properties as
// map[string]any. CallToolParams declares Arguments as any and RawArguments
// as json.RawMessage. BindArguments decodes nonempty RawArguments first.
// Otherwise it decodes json.RawMessage in Arguments, or marshals other
// Arguments values before decoding them. GetArguments provides access to
// Arguments as map[string]any for compatibility and returns nil for other
// types. Tack's schema builder creates property maps, and its argument
// handlers decode typed inputs or raw JSON values.
package tools

import (
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
)

// schemaType specifies a JSON Schema field type for tool input.
type schemaType string

const (
	schemaString  schemaType = "string"
	schemaObject  schemaType = "object"
	schemaInteger schemaType = "integer"
	schemaBoolean schemaType = "boolean"
)

// schemaField defines one tool input field. Desc and Enum are optional.
// Enum supplies string choices for schemaString fields. The builder includes
// Enum whenever it contains values.
type schemaField struct {
	Name string
	Type schemaType
	Desc string
	Enum []string
}

// schema builds an MCP input schema from typed fields. Registration calls
// toMCP to create the external ToolInputSchema.Properties map.
type schema struct {
	Fields   []schemaField
	Required []string
}

// toMCP converts typed fields to mcp-go's ToolInputSchema. It creates the
// property maps that the external schema type requires.
func (s schema) toMCP() mcpmcp.ToolInputSchema {
	props := make(map[string]any, len(s.Fields))
	for _, f := range s.Fields {
		spec := map[string]any{"type": string(f.Type)}
		if f.Desc != "" {
			spec["description"] = f.Desc
		}
		if len(f.Enum) > 0 {
			spec["enum"] = f.Enum
		}
		props[f.Name] = spec
	}
	return mcpmcp.ToolInputSchema{
		Type:                 "object",
		Properties:           props,
		Required:             s.Required,
		AdditionalProperties: false,
	}
}
