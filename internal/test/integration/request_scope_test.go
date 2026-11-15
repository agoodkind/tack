package integration_test

import (
	"testing"

	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	appruntime "goodkind.io/tack/internal/runtime"
)

// TestRuntimeAuditRoutineRequestsWithoutReadStorage exercises health checks,
// diagnostics, and discovery without an audit read lifecycle.
func TestRuntimeAuditRoutineRequestsWithoutReadStorage(t *testing.T) {
	server := startPublicServer(t, nil)
	recorder := mcpadapter.NewRequestRecorder(nil)

	server.handler = appruntime.NewRoutes(
		recorder,
		recorder.MCP(server.graph.AuthMiddleware, server.graph.MCPHandler),
		server.graph.Routes,
	)
	for _, current := range protocolCases(t, server) {
		if current.verb != "" {
			continue
		}

		t.Run(
			current.name,
			func(t *testing.T) {
				response := server.send(
					t,
					current.method,
					current.path,
					current.bearer,
					newRequestID(t, "routine"),
					current.body,
				)
				if response.status != current.status {
					t.Fatalf(
						"routine request returned HTTP %d, want %d: %s",
						response.status,
						current.status,
						response.body,
					)
				}
			},
		)
	}
}
