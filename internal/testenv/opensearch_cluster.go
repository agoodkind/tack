package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"

	"github.com/moby/moby/client"
)

// OpenSearchCluster is one disposable OpenSearch cluster behind a real
// Traefik proxy. Clients use the proxy as their only endpoint, as the
// application in each environment uses that environment's search proxy.
// Members join in a planned order up to the planned count.
type OpenSearchCluster struct {
	// Fixture stores the proxy endpoint, the cluster CA, and the admin login.
	Fixture OpenSearchFixture
	// name is the cluster name and the backend name that every member
	// certificate includes and the proxy verifies.
	name      string
	planned   []string
	members   []string
	stopped   map[string]bool
	proxy     string
	authority clusterAuthority
	users     []byte
}

// StartOpenSearchCluster starts the first of planned members and the proxy in
// front of it. It returns once an authenticated health request through the
// proxy succeeds.
func StartOpenSearchCluster(t T, planned int) *OpenSearchCluster {
	t.Helper()
	cluster := &OpenSearchCluster{
		Fixture:   OpenSearchFixture{Endpoint: "", CA: "", Username: "", Password: "", Container: ""},
		name:      "",
		planned:   nil,
		members:   nil,
		stopped:   map[string]bool{},
		proxy:     "",
		authority: clusterAuthority{certificate: nil, key: nil, pem: nil},
		users:     nil,
	}
	cluster.run(t, func(ctx context.Context, cli *client.Client) error { return cluster.start(ctx, cli, planned) })
	return cluster
}

// AddMember starts the next planned member, waits until the cluster lists it
// and the proxy reports it UP, and returns its container name.
func (c *OpenSearchCluster) AddMember(t T) string {
	t.Helper()
	member := ""
	c.run(t, func(ctx context.Context, cli *client.Client) error {
		joined, err := c.join(ctx, cli)
		if err != nil {
			return err
		}
		member = joined
		if err := writeContainerFile(ctx, cli, c.proxy, traefikDynamicPath, c.proxyConfig()); err != nil {
			return err
		}
		if err := c.waitForProxy(ctx); err != nil {
			return err
		}
		return c.waitForClusterSize(ctx)
	})
	return member
}

// StopMember kills member's container and waits until the proxy reports the
// member DOWN.
func (c *OpenSearchCluster) StopMember(t T, member string) {
	t.Helper()
	c.run(t, func(ctx context.Context, cli *client.Client) error {
		timeout := 0
		if _, err := cli.ContainerStop(ctx, member, client.ContainerStopOptions{Signal: "", Timeout: &timeout}); err != nil {
			slog.ErrorContext(ctx, "testenv.cluster.stop_failed", slog.String("err", err.Error()), slog.String("container", member))
			return fmt.Errorf("stop OpenSearch member %s: %w", member, err)
		}
		c.stopped[member] = true
		return c.waitForProxy(ctx)
	})
}

// StartMember starts member's stopped container and waits until the member
// answers an authenticated health request and the proxy reports it UP.
func (c *OpenSearchCluster) StartMember(t T, member string) {
	t.Helper()
	c.run(t, func(ctx context.Context, cli *client.Client) error {
		if _, err := cli.ContainerStart(ctx, member, client.ContainerStartOptions{}); err != nil {
			slog.ErrorContext(ctx, "testenv.cluster.restart_failed", slog.String("err", err.Error()), slog.String("container", member))
			return fmt.Errorf("start OpenSearch member %s: %w", member, err)
		}
		delete(c.stopped, member)
		if err := waitForOpenSearchHealth(ctx, c.memberFixture(member)); err != nil {
			return err
		}
		return c.waitForProxy(ctx)
	})
}

// Members returns the container names of every joined member in join order.
// The result includes a member that StopMember stopped.
func (c *OpenSearchCluster) Members() []string {
	return slices.Clone(c.members)
}

// MemberEndpoint returns the HTTPS endpoint the proxy uses for member.
func (c *OpenSearchCluster) MemberEndpoint(member string) string {
	return "https://" + member + ":9200"
}

// run calls step with a Docker client and a context that ends at the
// provisioning deadline. It fails t with the error when step fails.
func (c *OpenSearchCluster) run(t T, step func(context.Context, *client.Client) error) {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchCluster(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err == nil {
		err = step(ctx, cli)
		_ = cli.Close()
	}
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", err)
		t.FailNow()
	}
}

// start plans the member and proxy names, creates the CA and the admin
// login, starts the first member and the proxy, and checks health through
// the proxy.
func (c *OpenSearchCluster) start(ctx context.Context, cli *client.Client, planned int) error {
	if planned < 1 {
		return fmt.Errorf("an OpenSearch cluster needs at least one planned member, not %d", planned)
	}
	if err := ensureNetwork(ctx, cli); err != nil {
		return err
	}
	if err := joinNetworkWhenContainerized(ctx, cli); err != nil {
		return err
	}
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		return err
	}
	c.name = "tack-testenv-search-" + strconv.Itoa(os.Getpid()) + "-" + suffix
	for number := 1; number <= planned; number++ {
		c.planned = append(c.planned, c.name+"-"+strconv.Itoa(number))
	}
	c.proxy = c.name + "-proxy"
	if c.authority, err = newClusterAuthority(ctx); err != nil {
		return err
	}
	pass, err := randomHex(ctx, 24)
	if err != nil {
		return err
	}
	if c.users, err = openSearchInternalUsers(ctx, openSearchAdminUser, pass); err != nil {
		return err
	}
	c.Fixture = OpenSearchFixture{Endpoint: c.MemberEndpoint(c.proxy), CA: string(c.authority.pem), Username: openSearchAdminUser, Password: pass, Container: c.proxy}
	if _, err := c.join(ctx, cli); err != nil {
		return err
	}
	if err := c.startProxy(ctx, cli); err != nil {
		return err
	}
	if err := c.waitForProxy(ctx); err != nil {
		return err
	}
	slog.InfoContext(ctx, "testenv.cluster.started", slog.String("cluster", c.name), slog.Int("planned", planned))
	return waitForOpenSearchHealth(ctx, c.Fixture)
}

// join starts the next planned member and waits until it answers an
// authenticated health request. A member answers only after it joins the
// cluster.
func (c *OpenSearchCluster) join(ctx context.Context, cli *client.Client) (string, error) {
	if len(c.members) == len(c.planned) {
		return "", fmt.Errorf("cluster %s already runs all %d planned members", c.name, len(c.planned))
	}
	member := c.planned[len(c.members)]
	if err := c.startMember(ctx, cli, member); err != nil {
		return "", err
	}
	c.members = append(c.members, member)
	return member, waitForOpenSearchHealth(ctx, c.memberFixture(member))
}
