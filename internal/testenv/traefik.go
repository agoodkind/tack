package testenv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	// traefikImage is the Traefik release that the test cluster runs as the
	// proxy in front of its members.
	traefikImage = "traefik:v3.0.4"
	// traefikDynamicPath is the file the proxy's file provider watches. A
	// rewrite changes the backend list without a proxy restart.
	traefikDynamicPath = "/etc/traefik/dynamic/search.yml"
	traefikCertsDir    = "/etc/traefik/certs"
	// traefikServiceAPI is the Traefik API path of the search service.
	traefikServiceAPI = ":8080/api/http/services/search@file"
	backendUp         = "UP"
	backendDown       = "DOWN"
)

// traefikDynamicFormat terminates client TLS with the proxy certificate,
// re-encrypts to each member, and verifies each member against the cluster CA
// under the shared backend name. The health check sends an authenticated
// cluster health request to each member. Traefik marks a member that fails
// the check DOWN and stops routing requests to it.
const traefikDynamicFormat = `http:
  routers:
    search:
      entryPoints: [search]
      rule: "PathPrefix(` + "`/`" + `)"
      service: search
      tls: {}
  services:
    search:
      loadBalancer:
        serversTransport: search
        healthCheck:
          path: /_cluster/health
          interval: 2s
          timeout: 1s
          headers:
            Authorization: "Basic %s"
        servers:
%s  serversTransports:
    search:
      serverName: %s
      rootCAs:
        - ` + traefikCertsDir + `/ca.pem
tls:
  stores:
    default:
      defaultCertificate:
        certFile: ` + traefikCertsDir + `/proxy.pem
        keyFile: ` + traefikCertsDir + `/proxy-key.pem
`

// traefikService is the part of the Traefik API service record the fixture
// reads: the configured servers and the health status of each.
type traefikService struct {
	LoadBalancer struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	} `json:"loadBalancer"`
	ServerStatus map[string]string `json:"serverStatus"`
}

// proxyConfig renders the dynamic configuration for every joined member,
// including a stopped member.
func (c *OpenSearchCluster) proxyConfig() []byte {
	var servers strings.Builder
	for _, member := range c.members {
		_, _ = fmt.Fprintf(&servers, "          - url: %s\n", c.MemberEndpoint(member))
	}
	login := base64.StdEncoding.EncodeToString([]byte(c.Fixture.Username + ":" + c.Fixture.Password))
	return fmt.Appendf(nil, traefikDynamicFormat, login, servers.String(), c.name)
}

// startProxy starts Traefik on the engines' network with a certificate for
// its own container name and the dynamic configuration of the joined members.
func (c *OpenSearchCluster) startProxy(ctx context.Context, cli *client.Client) error {
	certificate, err := c.authority.issue(ctx, c.proxy, []string{c.proxy})
	if err != nil {
		return err
	}
	files := map[string][]byte{traefikDynamicPath: c.proxyConfig()}
	files[traefikCertsDir+"/ca.pem"] = c.authority.pem
	files[traefikCertsDir+"/proxy.pem"] = certificate.certificate
	files[traefikCertsDir+"/proxy-key.pem"] = certificate.key
	command := []string{
		"--entryPoints.search.address=:9200", "--entryPoints.traefik.address=:8080",
		"--api.insecure=true", "--providers.file.directory=/etc/traefik/dynamic",
		"--providers.file.watch=true", "--log.level=INFO",
	}
	return startClusterContainer(ctx, cli, clusterContainer{name: c.proxy, image: traefikImage, cmd: command, env: nil, files: files, owner: fileOwner{uid: 0, gid: 0}, resources: container.Resources{}})
}

// Backends returns each server URL the proxy lists and its health status.
func (c *OpenSearchCluster) Backends(t T) map[string]string {
	t.Helper()
	backends := map[string]string{}
	c.run(t, func(ctx context.Context, _ *client.Client) error {
		read, err := c.proxyBackends(ctx)
		backends = read
		return err
	})
	return backends
}

// proxyBackends reads each configured server URL and its health status from
// the Traefik API. A server without a reported status maps to an empty string.
func (c *OpenSearchCluster) proxyBackends(ctx context.Context) (map[string]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.proxy+traefikServiceAPI, nil)
	if err != nil {
		slog.DebugContext(ctx, "testenv.proxy.request_failed", slog.String("reason", err.Error()))
		return nil, errors.New("build Traefik API request: " + err.Error())
	}
	response, err := (&http.Client{Timeout: openSearchProbeTimeout}).Do(request)
	if err != nil {
		slog.DebugContext(ctx, "testenv.proxy.unavailable", slog.String("reason", err.Error()))
		return nil, errors.New("read Traefik API: " + err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("the Traefik API returned " + response.Status)
	}
	var service traefikService
	if err := json.NewDecoder(response.Body).Decode(&service); err != nil {
		slog.DebugContext(ctx, "testenv.proxy.decode_failed", slog.String("reason", err.Error()))
		return nil, errors.New("decode Traefik API service: " + err.Error())
	}
	backends := make(map[string]string, len(service.LoadBalancer.Servers))
	for _, server := range service.LoadBalancer.Servers {
		backends[server.URL] = service.ServerStatus[server.URL]
	}
	return backends, nil
}

// waitForProxy polls the Traefik API until the proxy lists exactly the
// joined members. The proxy must report each running member UP and each
// stopped member DOWN.
func (c *OpenSearchCluster) waitForProxy(ctx context.Context) error {
	want := make(map[string]string, len(c.members))
	for _, member := range c.members {
		want[c.MemberEndpoint(member)] = backendUp
		if c.stopped[member] {
			want[c.MemberEndpoint(member)] = backendDown
		}
	}
	for {
		backends, err := c.proxyBackends(ctx)
		if err == nil && maps.Equal(backends, want) {
			return nil
		}
		last := fmt.Sprint(backends)
		if err != nil {
			last = err.Error()
		}
		if !sleepOrDone(ctx) {
			wrapped := fmt.Errorf("wait for proxy %s backends %v, last %s: %w", c.proxy, want, last, ctx.Err())
			slog.ErrorContext(ctx, "testenv.proxy.wait_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
}
