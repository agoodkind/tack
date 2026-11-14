// Package tools defines typed schemas and responses for MCP tools. It converts
// these schemas to mark3labs/mcp-go's map[string]any representation when
// registering tools. Other callers in the package use typed structs.
package tools

import (
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
)

// schemaType enumerates the JSON-schema field types for tool input.
type schemaType string

const (
	schemaString  schemaType = "string"
	schemaObject  schemaType = "object"
	schemaInteger schemaType = "integer"
	schemaBoolean schemaType = "boolean"
)

// schemaField describes one field in a tool's input schema. Description and
// Enum are optional. Enum is only meaningful for schemaString fields.
type schemaField struct {
	Name string
	Type schemaType
	Desc string
	Enum []string
}

// schema is a typed builder for an MCP tool input schema. Call toMCP to
// convert at registration time. The any-typed result lives entirely inside
// mcp-go's ToolInputSchema.Properties field; Tack code outside this file
// never constructs that shape directly.
type schema struct {
	Fields   []schemaField
	Required []string
}

// toMCP converts the typed schema to mcp-go's ToolInputSchema. This is the
// single bridge point between Tack's typed builder and the external map
// shape mcp-go declares.
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
