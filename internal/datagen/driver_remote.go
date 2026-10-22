package datagen

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"goodkind.io/tack/internal/runtime"
)

var forwardedRequestHeaders = []string{
	"Authorization",
	"Content-Type",
	"Accept",
	"Mcp-Session-Id",
	"Mcp-Protocol-Version",
}

type remoteForwarder struct {
	target string
	client *http.Client
}

// NewRemoteDriver creates a Driver for MCP requests to a remote Tack process.
func NewRemoteDriver(endpoint string, client *http.Client, seed int64) (*Driver, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("qa datagen: remote endpoint %q is not a valid URL", endpoint)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("qa datagen: remote endpoint scheme %q is not http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("qa datagen: remote endpoint %q has no host", endpoint)
	}
	forwarder := &remoteForwarder{
		target: parsed.JoinPath("mcp").String(),
		client: client,
	}
	// The forwarding graph uses identity middleware because the remote process
	// authenticates the bearer token.
	graph := &runtime.Graph{
		MCPHandler:     forwarder,
		AuthMiddleware: identityMiddleware,
	}
	return NewDriver(graph, false, seed), nil
}

func identityMiddleware(next http.Handler) http.Handler {
	return next
}

func (f *remoteForwarder) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	outbound, err := http.NewRequestWithContext(ctx, request.Method, f.target, request.Body)
	if err != nil {
		slog.ErrorContext(ctx, "remote_request.build_failed", slog.String("err", err.Error()))
		writer.WriteHeader(http.StatusBadGateway)
		return
	}
	for _, name := range forwardedRequestHeaders {
		if value := request.Header.Get(name); value != "" {
			outbound.Header.Set(name, value)
		}
	}
	response, err := f.client.Do(outbound)
	if err != nil {
		slog.ErrorContext(ctx, "remote_request.send_failed", slog.String("err", err.Error()))
		writer.WriteHeader(http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		writer.Header()[name] = values
	}
	writer.WriteHeader(response.StatusCode)
	if _, err := io.Copy(writer, response.Body); err != nil {
		slog.ErrorContext(ctx, "remote_response.copy_failed", slog.String("err", err.Error()))
	}
}
