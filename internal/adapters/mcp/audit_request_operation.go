package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"goodkind.io/tack/internal/audit"
)

const (
	mcpRoute = "/mcp"
	// maxOperationNameBytes limits the byte length of tool and prompt names in read events.
	maxOperationNameBytes = 128
	// mcpMethodToolsCall is the JSON-RPC method that calls a tool.
	mcpMethodToolsCall = "tools/call"
	// workspaceListTool is the tool that lists the workspaces of every member organization.
	workspaceListTool = "tack_list_workspaces"
)

// mcpMethod specifies the audit verb and organization source for one MCP method.
// metadata marks a method with a response that the server builds from the node
// types and property definitions of every member organization.
type mcpMethod struct {
	verb          audit.Verb
	organizations orgSource
	metadata      bool
}

// mcpMethods maps MCP methods to audit verbs for read events.
var mcpMethods = map[string]mcpMethod{
	"initialize":                {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"ping":                      {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"tools/list":                {verb: audit.VerbMCPProtocolRead, organizations: orgCaller, metadata: true},
	"prompts/list":              {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"resources/list":            {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"resources/templates/list":  {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"notifications/initialized": {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"notifications/cancelled":   {verb: audit.VerbMCPProtocolRead, organizations: orgSystem, metadata: false},
	"prompts/get":               {verb: audit.VerbMCPPromptInvoked, organizations: orgCaller, metadata: true},
	"resources/read":            {verb: audit.VerbMCPResourceRead, organizations: orgCaller, metadata: true},
}

// RecordedMethods returns the MCP JSON-RPC methods that the request recorder
// records, sorted by name.
func RecordedMethods() []string {
	methods := make([]string, 0, len(mcpMethods)+1)
	for method := range mcpMethods {
		methods = append(methods, method)
	}
	methods = append(methods, mcpMethodToolsCall)
	slices.Sort(methods)
	return methods
}

// requestOperation identifies the operation of one HTTP request.
// Its route, method, and tool fields contain a route pattern, a known method name,
// and a checked tool or prompt name. The recorder reads arguments only to derive
// the organization and does not store them. selection contains the normalized
// arguments of a read tool call or prompt.
type requestOperation struct {
	verb          audit.Verb
	route         string
	method        string
	tool          string
	source        audit.Source
	mutation      bool
	organizations orgSource
	metadata      bool
	arguments     json.RawMessage
	selection     *audit.ReadSelection
}

func routeOperation(verb audit.Verb, route string) requestOperation {
	return requestOperation{
		verb: verb, route: route, method: "", tool: "", source: audit.SourceSystem, mutation: false,
		organizations: orgSystem, metadata: false, arguments: nil, selection: nil,
	}
}

// mcpOperation reads the JSON-RPC method and tool or prompt name, then restores the request body.
// It returns an operation with audit.VerbMCPRequestRejected if the body does not decode
// as one JSON-RPC request. If reading the body fails, it supplies an empty body to the MCP server.
func mcpOperation(request *http.Request) requestOperation {
	operation := routeOperation(audit.VerbMCPRequestRejected, mcpRoute)
	operation.source = audit.SourceMCP
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
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &message) != nil {
		return operation
	}
	if message.Method == mcpMethodToolsCall {
		return toolOperation(operation, checkedName(message.Params.Name), message.Params.Arguments)
	}
	method, known := mcpMethods[message.Method]
	if !known {
		return operation
	}
	operation.verb, operation.method = method.verb, message.Method
	operation.organizations, operation.metadata = method.organizations, method.metadata
	if method.verb == audit.VerbMCPPromptInvoked {
		operation.tool = checkedName(message.Params.Name)
		operation.selection = readSelection(message.Params.Arguments)
	}
	return operation
}

// toolOperation returns the operation for a tool call and marks tools that change state as mutations.
// The workspace list tool uses every member organization. Other read tools use the
// organization of the workspace or node that their arguments identify.
func toolOperation(operation requestOperation, tool string, arguments json.RawMessage) requestOperation {
	operation.method, operation.tool = mcpMethodToolsCall, tool
	verb, covered := audit.ToolVerb(tool)
	if tool == "" || !covered || verb == "" {
		return operation
	}
	operation.verb = verb
	operation.mutation = !audit.IsRead(verb)
	operation.organizations, operation.arguments = orgArguments, arguments
	if !operation.mutation {
		operation.selection = readSelection(arguments)
	}
	if tool == workspaceListTool {
		operation.organizations = orgCaller
	}
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
