package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"goodkind.io/tack/internal/audit"
)

const (
	// mcpRoute is the route of the MCP endpoint.
	mcpRoute = "/mcp"
	// maxOperationNameBytes limits the byte length of tool and prompt names in read events.
	maxOperationNameBytes = 128
	// mcpMethodToolsCall is the JSON-RPC method that calls a tool.
	mcpMethodToolsCall = "tools/call"
)

// mcpMethodVerbs maps MCP methods to audit verbs for read events.
var mcpMethodVerbs = map[string]audit.Verb{
	"initialize":                audit.VerbMCPProtocolRead,
	"ping":                      audit.VerbMCPProtocolRead,
	"tools/list":                audit.VerbMCPProtocolRead,
	"prompts/list":              audit.VerbMCPProtocolRead,
	"resources/list":            audit.VerbMCPProtocolRead,
	"resources/templates/list":  audit.VerbMCPProtocolRead,
	"notifications/initialized": audit.VerbMCPProtocolRead,
	"notifications/cancelled":   audit.VerbMCPProtocolRead,
	"prompts/get":               audit.VerbMCPPromptInvoked,
	"resources/read":            audit.VerbMCPResourceRead,
}

// requestOperation identifies the operation of one HTTP request.
// Its route, method, and tool fields contain a route pattern, a known method name,
// and a checked tool or prompt name.
type requestOperation struct {
	verb     audit.Verb
	route    string
	method   string
	tool     string
	source   audit.Source
	mutation bool
}

// routeOperation returns the operation of a route that does not use MCP.
func routeOperation(verb audit.Verb, route string) requestOperation {
	return requestOperation{verb: verb, route: route, method: "", tool: "", source: audit.SourceSystem, mutation: false}
}

// mcpOperation reads the JSON-RPC method and tool or prompt name, then restores the request body.
// It returns an operation with audit.VerbMCPRequestRejected if the body does not decode
// as one JSON-RPC request. If reading the body fails, it supplies an empty body to the MCP server.
func mcpOperation(request *http.Request) requestOperation {
	operation := requestOperation{
		verb: audit.VerbMCPRequestRejected, route: mcpRoute, method: "", tool: "", source: audit.SourceMCP, mutation: false,
	}
	if request.Method != http.MethodPost || request.Body == nil {
		return operation
	}
	body, err := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		slog.ErrorContext(request.Context(), "mcp.audit_request.body_read_failed", slog.String("err", err.Error()))
		request.Body = io.NopCloser(bytes.NewReader(nil))
		return operation
	}
	var message struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &message) != nil {
		return operation
	}
	if message.Method == mcpMethodToolsCall {
		return toolOperation(operation, checkedName(message.Params.Name))
	}
	verb, known := mcpMethodVerbs[message.Method]
	if !known {
		return operation
	}
	operation.verb, operation.method = verb, message.Method
	if verb == audit.VerbMCPPromptInvoked {
		operation.tool = checkedName(message.Params.Name)
	}
	return operation
}

// toolOperation returns the operation for a tool call and marks tools that change state as mutations.
func toolOperation(operation requestOperation, tool string) requestOperation {
	operation.method, operation.tool = mcpMethodToolsCall, tool
	verb, covered := audit.ToolVerb(tool)
	if tool == "" || !covered || verb == "" {
		return operation
	}
	operation.verb = verb
	operation.mutation = !audit.IsRead(verb)
	return operation
}

// checkedName returns name when it contains only lowercase letters, digits,
// and underscores within maxOperationNameBytes. It returns an empty string otherwise.
func checkedName(name string) string {
	if name == "" || len(name) > maxOperationNameBytes {
		return ""
	}
	for _, character := range name {
		lower := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		if !lower && !digit && character != '_' {
			return ""
		}
	}
	return name
}
