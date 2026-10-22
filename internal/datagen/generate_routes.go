package datagen

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
)

// routeProbe contains an HTTP request and its expected response status.
type routeProbe struct {
	label         string
	method        string
	path          string
	authenticated bool
	body          string
	status        int
}

// routeProbes contains the requests that produce each request recorder verb.
var routeProbes = []routeProbe{
	{"health check", http.MethodGet, "/healthz", false, "", http.StatusOK},
	{"health check method", http.MethodPost, "/healthz", false, "", http.StatusMethodNotAllowed},
	{"diagnostics", http.MethodGet, "/debug/vars", false, "", http.StatusOK},
	{"unregistered route", http.MethodGet, "/qa-datagen-unregistered", false, "", http.StatusNotFound},
	{"MCP GET", http.MethodGet, "/mcp", false, "", http.StatusMethodNotAllowed},
	{"initialize", http.MethodPost, "/mcp", true,
		`{"jsonrpc":"2.0","id":"qa-initialize","method":"initialize","params":{"protocolVersion":"2025-06-18",` +
			`"capabilities":{},"clientInfo":{"name":"qa-datagen","version":"1"}}}`, http.StatusOK},
	{"ping", http.MethodPost, "/mcp", true, `{"jsonrpc":"2.0","id":"qa-ping","method":"ping","params":{}}`, http.StatusOK},
	{"tools/list", http.MethodPost, "/mcp", true, `{"jsonrpc":"2.0","id":"qa-tools","method":"tools/list","params":{}}`, http.StatusOK},
	{"prompts/get", http.MethodPost, "/mcp", true,
		`{"jsonrpc":"2.0","id":"qa-prompt","method":"prompts/get","params":{"name":"tack_getting_started"}}`, http.StatusOK},
	{"unknown method", http.MethodPost, "/mcp", true, `{"jsonrpc":"2.0","id":"qa-unknown","method":"qa/unknown","params":{}}`, http.StatusOK},
	{"unknown tool", http.MethodPost, "/mcp", true,
		`{"jsonrpc":"2.0","id":"qa-tool","method":"tools/call","params":{"name":"qa_unknown_tool","arguments":{}}}`, http.StatusOK},
	{"malformed body", http.MethodPost, "/mcp", true, `{"jsonrpc":"2.0","id":`, http.StatusBadRequest},
}

// exerciseHTTPRoutes runs each route probe against the server routes and checks the response status.
// It uses the first actor of the first workspace for probes that require authentication.
func (g *Generator) exerciseHTTPRoutes(ctx context.Context) error {
	if g.dryRun {
		slog.InfoContext(ctx, "qa.datagen.route_probes_planned", slog.Int("probes", len(routeProbes)))
		return nil
	}
	if g.driver.graph == nil || g.driver.graph.Routes == nil {
		return loggedError(ctx, "qa datagen: route probes", fmt.Errorf("the runtime graph does not provide HTTP routes"))
	}
	if len(g.identities.Workspaces) == 0 || len(g.identities.Workspaces[0].Actors) == 0 {
		return loggedError(ctx, "qa datagen: route probes", fmt.Errorf("the scale does not generate an actor"))
	}
	token := g.identities.Workspaces[0].Actors[0].Token
	for _, probe := range routeProbes {
		request := httptest.NewRequestWithContext(ctx, probe.method, probe.path, bytes.NewReader([]byte(probe.body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if probe.authenticated {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		g.driver.graph.Routes.ServeHTTP(recorder, request)
		if recorder.Code != probe.status {
			return loggedError(ctx, "qa datagen: route probe "+probe.label,
				fmt.Errorf("response status %d, want %d", recorder.Code, probe.status))
		}
	}
	slog.InfoContext(ctx, "qa.datagen.route_probes_completed", slog.Int("probes", len(routeProbes)))
	return nil
}
