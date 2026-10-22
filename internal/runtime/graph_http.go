package runtime

import (
	"context"
	"expvar"
	"net/http"

	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
)

// NewRoutes registers the server's HTTP routes.
// The request recorder saves read events for health checks, diagnostics, and unregistered paths.
// mcpRoute serves the MCP endpoint and records its reads with the same recorder.
func NewRoutes(requests *mcpadapter.RequestRecorder, mcpRoute, health http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpRoute)
	mux.Handle("/mcp/", mcpRoute)
	mux.Handle("/healthz", requests.Health(health))
	// Local monitoring reads the expvar counters at /debug/vars without authentication.
	// Bind the server to localhost when the counters must remain private.
	mux.Handle("/debug/vars", requests.Diagnostics(expvar.Handler()))
	mux.Handle("/", requests.UnknownRoute())
	return mux
}

// healthHandler returns the health check of the YugabyteDB and FoundationDB stores of g.
func (g *Graph) healthHandler() http.Handler {
	return NewHealthHandler(map[string]func(context.Context) error{
		"yugabyte": g.PingYugabyte,
		"fdb":      g.PingFoundationDB,
	})
}
