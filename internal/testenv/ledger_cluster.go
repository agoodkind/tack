package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
)

// LedgerCluster is a set of YugabyteDB nodes that one test starts under
// fixed container names, the way each environment starts yb1, yb2, and yb3.
// Every node runs the stack file's image with the overlay yugabyted and the
// same superuser login. The first node bootstraps the universe and each later
// node joins a named node.
type LedgerCluster struct {
	// Network is the Docker network every node and every test engine joins.
	Network string
	// Image is the stack file's yugabyte image the nodes run.
	Image string

	cli          *client.Client
	platform     *ocispec.Platform
	overlay      []byte
	superuserKey string
	names        []string
	started      []string
}

// NewLedgerCluster prepares nodes under names without starting any. The test
// fails when a container with any of the names exists, and every node it
// starts is removed when the test ends.
func NewLedgerCluster(t *testing.T, names ...string) *LedgerCluster {
	t.Helper()
	skipWhenShort(t)
	ctx := t.Context()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("ledger cluster: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	for _, name := range names {
		if err := refuseExistingContainer(ctx, cli, name); err != nil {
			t.Fatalf("ledger cluster: %v", err)
		}
	}
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
	cluster := &LedgerCluster{
		Network: networkName, Image: image, cli: cli, platform: platform, overlay: overlay,
		superuserKey: superuserKey, names: names, started: nil,
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		if err := removeContainers(cleanup, cluster.started); err != nil {
			t.Errorf("remove ledger cluster nodes: %v", err)
		}
	})
	return cluster
}

// Start starts the node named name. An empty joinTarget bootstraps the
// universe, and Start then waits until the node answers SQL. A node that
// joins is returned as soon as its container runs; the caller waits for it.
func (c *LedgerCluster) Start(t *testing.T, name, joinTarget string) EmptyLedgerNode {
	t.Helper()
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
		kind: ledgerService, image: c.Image, platform: c.platform, cmd: command,
		env: []string{
			"YSQL_USER=" + ledgerAdminUser, "YSQL_PASSWORD=" + c.superuserKey, "YSQL_DB=" + ledgerDatabase,
		},
		files: map[string][]byte{ledgerClusterYugabyted: c.overlay},
		name:  name,
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
	slog.InfoContext(ctx, "testenv.ledger_cluster.node_started",
		slog.String("container", name), slog.String("join", joinTarget))
	return EmptyLedgerNode{
		DSN: dsn.String(), Address: started.address,
		MasterAddress: net.JoinHostPort(started.address, ledgerMasterPort), Network: networkName,
	}
}

// KeywordDSN returns a keyword connection string with host= listing every
// node name of the cluster in order, for login with secret.
func (c *LedgerCluster) KeywordDSN(login, secret string) string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		strings.Join(c.names, ","), ledgerPort, login, secret, ledgerDatabase)
}

// AdminSecret returns the superuser key every node was started with.
func (c *LedgerCluster) AdminSecret() string {
	return c.superuserKey
}

// Admin runs one yb-admin subcommand in the first node against the masters
// of the started nodes and returns its output. A non-zero exit is an error.
func (c *LedgerCluster) Admin(ctx context.Context, subcommand string) (string, error) {
	addresses := make([]string, 0, len(c.started))
	for _, name := range c.started {
		addresses = append(addresses, name+":"+ledgerMasterPort)
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	command := []string{ledgerClusterAdmin, "--master_addresses", strings.Join(addresses, ","), subcommand}
	output, code, err := execInContainer(readCtx, c.cli, c.names[0], command)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return output, fmt.Errorf("yb-admin %s exited %d: %s", subcommand, code, output)
	}
	return output, nil
}
