package runtime

import (
	"context"
	"expvar"
	"net/http"

	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
)

// Route pairs an HTTP route pattern with its handler.
type Route struct {
	Pattern string
	Handler http.Handler
}

// NewRoutes creates an [http.ServeMux] and registers every [RouteTable] entry
// on it.
func NewRoutes(requests *mcpadapter.RequestRecorder, mcpRoute, health http.Handler, authMiddleware func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	for _, route := range RouteTable(requests, mcpRoute, health, authMiddleware) {
		mux.Handle(route.Pattern, route.Handler)
	}
	return mux
}

// RouteTable returns the server's HTTP route patterns and handlers. It uses
// mcpRoute for MCP requests and wraps health checks, diagnostics, and unmatched
// paths with the request recorder.
func RouteTable(requests *mcpadapter.RequestRecorder, mcpRoute, health http.Handler, authMiddleware func(http.Handler) http.Handler) []Route {
	routes := []Route{
		{Pattern: "/mcp", Handler: mcpRoute},
		{Pattern: "/mcp/", Handler: mcpRoute},
		{Pattern: "/healthz", Handler: requests.Health(health)},
		// Local monitoring reads the expvar counters at /debug/vars without authentication.
		// Bind the server to localhost when the counters must remain private.
		{Pattern: "/debug/vars", Handler: requests.Diagnostics(expvar.Handler())},
	}
	routes = append(routes, connectRoutes(authMiddleware)...)
	return append(routes, Route{Pattern: "/", Handler: requests.UnknownRoute()})
}

// connectRoutes returns nil because the server does not register any
// Connect-RPC routes.
func connectRoutes(_ func(http.Handler) http.Handler) []Route {
	return nil
}

// RouteTable returns the HTTP route patterns and handlers that NewRoutes
// registers for this graph.
func (g *Graph) RouteTable() []Route {
	return RouteTable(g.Requests, g.MCPRoute, g.healthHandler(), g.AuthMiddleware)
}

func (g *Graph) healthHandler() http.Handler {
	return NewHealthHandler(map[string]func(context.Context) error{
		"yugabyte": g.PingYugabyte,
		"fdb":      g.PingFoundationDB,
	})
}
