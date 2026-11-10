package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// ledgerClusterOverlay is the patched yugabyted every environment
	// bind-mounts, relative to the repository root.
	ledgerClusterOverlay = "yugabyte-overlay/yugabyted"
	// ledgerClusterYugabyted is where the stack file mounts the overlay.
	ledgerClusterYugabyted = "/home/yugabyte/bin/yugabyted"
	// ledgerClusterAdmin is the engine's administration tool in the image.
	ledgerClusterAdmin = "/home/yugabyte/bin/yb-admin"
	// ledgerClusterNodeKind prefixes a node name to form the node's kind in
	// its generated container name.
	ledgerClusterNodeKind = "ledger-"
)

// LedgerCluster is a set of YugabyteDB nodes that one test starts on a
// Docker network of its own, the way each environment runs yb1, yb2, and yb3
// on separate guests. Each node runs under a generated container name with a
// fixed IPv4 address, no network alias, and one hosts entry per node name.
// The node names resolve inside the nodes through /etc/hosts and through no
// DNS. Every node runs the stack file's image with the overlay yugabyted and
// the same superuser login. The first node bootstraps the universe and each
// later node joins a named node.
type LedgerCluster struct {
	// Network is the cluster's own Docker network. The nodes and the test
	// process join it.
	Network string
	// Image is the stack file's yugabyte image the nodes run.
	Image string

	cli          *client.Client
	platform     *ocispec.Platform
	overlay      []byte
	superuserKey string
	selfID       string
	names        []string
	addresses    map[string]netip.Addr
	containers   map[string]string
	extraHosts   []string
	started      []string
}

// NewLedgerCluster creates the cluster network, joins the test process to it
// when the process runs in a container, and plans one node per name without
// starting any. Every node it starts and the network are removed when the
// test ends.
func NewLedgerCluster(t *testing.T, names ...string) *LedgerCluster {
	t.Helper()
	skipWhenShort(t)
	ctx := t.Context()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	image, err := serviceImage(ctx, ledgerService)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	platform, err := ledgerPlatform(ctx, cli)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	root, err := repoRoot(ctx)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	overlay, err := os.ReadFile(filepath.Join(root, ledgerClusterOverlay))
	if err != nil {
		t.Fatalf("ledger cluster: read %s: %v", ledgerClusterOverlay, err)
	}
	superuserKey, err := randomHex(ctx, 16)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	clusterNetwork, subnet, err := createLedgerClusterNetwork(ctx, cli)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	cluster := &LedgerCluster{
		Network: clusterNetwork, Image: image, cli: cli, platform: platform, overlay: overlay,
		superuserKey: superuserKey, selfID: "", names: names, addresses: map[string]netip.Addr{},
		containers: map[string]string{}, extraHosts: nil, started: nil,
	}
	t.Cleanup(func() { cluster.remove(t) })
	if cluster.selfID, err = joinWhenContainerized(ctx, cli, clusterNetwork); err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	if err := cluster.plan(ctx, subnet); err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	return cluster
}

// plan gives each node a generated container name and a fixed address in
// the lower half of subnet, from host number ledgerClusterFirstNodeHost up.
func (c *LedgerCluster) plan(ctx context.Context, subnet netip.Prefix) error {
	if len(c.names) > ledgerClusterDynamicHost-ledgerClusterFirstNodeHost {
		return fmt.Errorf("%d ledger nodes do not fit below the dynamic range of %s", len(c.names), subnet)
	}
	var host uint32 = ledgerClusterFirstNodeHost
	for _, name := range c.names {
		containerName, err := generatedEngineName(ctx, ledgerClusterNodeKind+name)
		if err != nil {
			return err
		}
		address := ledgerClusterHost(subnet, host)
		c.containers[name], c.addresses[name] = containerName, address
		c.extraHosts = append(c.extraHosts, name+":"+address.String())
		host++
	}
	return nil
}

// Start starts the node named name at its fixed address. An empty joinTarget
// bootstraps the universe, and Start then waits until the node answers SQL.
// A node that joins is returned as soon as its container runs; the caller
// waits for it.
func (c *LedgerCluster) Start(t *testing.T, name, joinTarget string) EmptyLedgerNode {
	t.Helper()
	containerName, planned := c.containers[name]
	if !planned {
		t.Fatalf("start ledger node %s: the cluster plans only %v", name, c.names)
	}
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	command := []string{
		"python3", ledgerClusterYugabyted, "start", "--daemon=false", "--base_dir=/home/yugabyte/var",
		"--advertise_address=" + name, "--listen=" + name,
		"--tserver_flags=ysql_num_shards_per_tserver=1,yb_num_shards_per_tserver=1",
	}
	if joinTarget != "" {
		command = append(command, "--join="+joinTarget)
	}
	started, err := startEngine(ctx, c.cli, engineSpec{
		kind: ledgerClusterNodeKind + name, image: c.Image, platform: c.platform, cmd: command,
		env: []string{
			"YSQL_USER=" + ledgerAdminUser, "YSQL_PASSWORD=" + c.superuserKey, "YSQL_DB=" + ledgerDatabase,
		},
		files: map[string][]byte{ledgerClusterYugabyted: c.overlay},
		name:  containerName, attachNetwork: c.Network, ipv4Address: c.addresses[name], extraHosts: c.extraHosts,
	})
	c.started = append(c.started, name)
	if err != nil {
		t.Fatalf("start ledger node %s: %v", name, err)
	}
	dsn := url.URL{
		Scheme: "postgres", User: url.UserPassword(ledgerAdminUser, c.superuserKey),
		Host: net.JoinHostPort(started.address, ledgerPort), Path: "/" + ledgerDatabase,
		RawQuery: "sslmode=disable",
	}
	if joinTarget == "" {
		if err := waitForLedger(ctx, dsn.String()); err != nil {
			t.Fatalf("start ledger node %s: %v", name, err)
		}
	}
	slog.InfoContext(ctx, "testenv.ledger_cluster.node_started", slog.String("node", name),
		slog.String("container", containerName), slog.String("address", started.address),
		slog.String("join", joinTarget))
	return EmptyLedgerNode{
		DSN: dsn.String(), Address: started.address,
		MasterAddress: net.JoinHostPort(started.address, ledgerMasterPort), Network: c.Network,
		Image: c.Image,
	}
}

// NodeAddresses returns the fixed IPv4 address of every planned node, keyed
// by node name. Every address is known before any node starts.
func (c *LedgerCluster) NodeAddresses() map[string]string {
	addresses := make(map[string]string, len(c.addresses))
	for name, address := range c.addresses {
		addresses[name] = address.String()
	}
	return addresses
}
