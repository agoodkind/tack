package audit

const (
	// VerbHTTPHealthRead is the audit verb for requests to the health check route.
	VerbHTTPHealthRead Verb = "http.health_read"
	// VerbHTTPDiagnosticsRead is the audit verb for requests to the diagnostics route.
	VerbHTTPDiagnosticsRead Verb = "http.diagnostics_read"
	// VerbHTTPRouteUnknown is the audit verb for requests to routes that the server does not register.
	VerbHTTPRouteUnknown Verb = "http.route_unknown"
	// VerbMCPProtocolRead is the audit verb for MCP protocol requests, such as initialize,
	// ping, or requests to list tools, prompts, or resources.
	VerbMCPProtocolRead Verb = "mcp.protocol_read"
	// VerbMCPRequestRejected is the audit verb for MCP requests with an unknown method,
	// an unknown tool, a malformed body, or an unsupported HTTP method.
	VerbMCPRequestRejected Verb = "mcp.request_rejected"
)
