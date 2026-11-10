package testenv

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/moby/moby/client"
	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"goodkind.io/tack/internal/adapters/search"
)

const (
	// traefikRefusedRouterFormat matches paths that start with refusedPath on the
	// search entry point and selects the refused service.
	// Its priority exceeds the search router's priority.
	traefikRefusedRouterFormat = `    refused:
      entryPoints: [search]
      rule: "PathPrefix(` + "`%s`" + `)"
      priority: 1000
      service: refused
      tls: {}
`
	// traefikRefusedServiceFormat lists every joined member and uses the refused
	// servers transport. It omits health checks, and Traefik keeps every member UP.
	traefikRefusedServiceFormat = `    refused:
      loadBalancer:
        serversTransport: refused
        servers:
%s`
	// traefikRefusedTransportFormat trusts the cluster CA but expects a server name
	// that member certificates do not contain. Traefik rejects each member's TLS
	// certificate and returns HTTP 500 with a plain text body.
	// OpenSearch does not receive the request.
	traefikRefusedTransportFormat = `    refused:
      serverName: %s-refused
      rootCAs:
        - ` + traefikCertsDir + `/ca.pem
`
	// proxyErrorBody is the plain text body Traefik writes with its own HTTP 500 response.
	proxyErrorBody = "Internal Server Error"
	// refusalBodyLimit limits how many response body bytes the readiness probe reads.
	refusalBodyLimit = 1024
)

// proxyRefusal stores the YAML fragments for the refused route.
// Its fields are empty when refusedPath is empty.
type proxyRefusal struct{ router, service, transport string }

// refusalConfig renders the refused route fragments for refusedPath using servers.
// It returns empty fragments when refusedPath is empty.
func (c *OpenSearchCluster) refusalConfig(servers string) proxyRefusal {
	if c.refusedPath == "" {
		return proxyRefusal{router: "", service: "", transport: ""}
	}
	return proxyRefusal{
		router:    fmt.Sprintf(traefikRefusedRouterFormat, c.refusedPath),
		service:   fmt.Sprintf(traefikRefusedServiceFormat, servers),
		transport: fmt.Sprintf(traefikRefusedTransportFormat, c.name),
	}
}

// RefusePath rewrites the proxy configuration to refuse paths that start with prefix.
// It waits for HTTP 500 with a body equal to [proxyErrorBody] and fails t if the
// provisioning deadline expires first. The proxy forwards other paths to OpenSearch.
func (c *OpenSearchCluster) RefusePath(t T, prefix string) {
	t.Helper()
	c.run(t, func(ctx context.Context, cli *client.Client) error {
		c.refusedPath = prefix
		if err := writeContainerFile(ctx, cli, c.proxy, traefikDynamicPath, c.proxyConfig()); err != nil {
			return err
		}
		return c.waitForRefusal(ctx)
	})
}

// waitForRefusal polls refusedPath through the proxy until it receives HTTP 500
// with a body equal to [proxyErrorBody]. If ctx ends first, it returns an error
// with the last observed failure.
func (c *OpenSearchCluster) waitForRefusal(ctx context.Context) error {
	pass := c.Fixture.Password
	configuration := search.Config{
		Endpoint: c.Fixture.Endpoint, CA: c.Fixture.CA, Username: c.Fixture.Username,
		Password: pass, RequestTimeout: openSearchProbeTimeout, MaxRetries: 0,
	}
	probe, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		wrapped := fmt.Errorf("create proxy refusal client: %w", err)
		slog.ErrorContext(ctx, "testenv.proxy.refusal_client_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer func() { _ = probe.Close() }()
	for {
		failure := refusalFailure(ctx, probe, c.refusedPath)
		if failure == "" {
			return nil
		}
		if !sleepOrDone(ctx) {
			wrapped := fmt.Errorf("wait for proxy %s to refuse %s, last %s: %w", c.proxy, c.refusedPath, failure, ctx.Err())
			slog.ErrorContext(ctx, "testenv.proxy.refusal_wait_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
}

// refusalFailure sends one GET request for path through probe.
// It returns an empty string when the response has HTTP 500 and a body equal to
// [proxyErrorBody]. Otherwise, it returns a description of the failure.
func refusalFailure(ctx context.Context, probe *opensearch.Client, path string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "build request: " + err.Error()
	}
	response, err := probe.Request(request)
	if err != nil {
		return "send request: " + err.Error()
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, refusalBodyLimit))
	if err != nil {
		return "read response: " + err.Error()
	}
	if response.StatusCode != http.StatusInternalServerError || string(body) != proxyErrorBody {
		return fmt.Sprintf("status %d with body %q", response.StatusCode, body)
	}
	return ""
}
