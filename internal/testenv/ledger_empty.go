package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	// ledgerMasterPort is the engine's master RPC port.
	ledgerMasterPort = "7100"
	// The health check timings match the yugabyte service in
	// docker-compose.yml.
	ledgerHealthInterval    = 10 * time.Second
	ledgerHealthTimeout     = 10 * time.Second
	ledgerHealthStartPeriod = 180 * time.Second
	ledgerHealthRetries     = 10
	// ledgerHealthTest is the yugabyte service's health check. It logs in
	// with the container's own environment on a first start and uses no login
	// when the container has no password.
	ledgerHealthTest = `if [ -n "$YSQL_PASSWORD" ]; then PGPASSWORD="$YSQL_PASSWORD" ysqlsh -h "$(hostname)" ` +
		`-p 5433 -U "$YSQL_USER" -d "$YSQL_DB" -c 'SELECT 1' -t; ` +
		`else /home/yugabyte/postgres/bin/pg_isready -h "$(hostname)" -p 5433; fi`
)

// EmptyLedgerNode is a YugabyteDB node that one test started. The node has
// no migrations, roles, or tables beyond the database the engine creates at
// start.
type EmptyLedgerNode struct {
	// DSN connects to the node as the engine superuser.
	DSN string
	// Address is the node's IP address on Network.
	Address string
	// MasterAddress is the node's master RPC address as host:port.
	MasterAddress string
	// Network is the Docker network that the node and every other test engine
	// join.
	Network string
	// Image is the engine image of the node.
	Image string
}

// StartEmptyLedger starts an unmigrated YugabyteDB node under containerName,
// with the health check docker-compose.yml declares for the yugabyte service,
// and removes it when the test ends. The node advertises and listens on
// containerName, the way the stack's node uses its service name. The test
// fails when a container already uses containerName. The test never adopts
// or removes a container that it did not start.
func StartEmptyLedger(t *testing.T, containerName string) EmptyLedgerNode {
	t.Helper()
	skipWhenShort(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("start empty ledger %s: %v", containerName, err)
	}
	defer func() { _ = cli.Close() }()
	if err := refuseExistingContainer(ctx, cli, containerName); err != nil {
		t.Fatalf("start empty ledger: %v", err)
	}
	superuserKey, err := randomHex(ctx, 16)
	if err != nil {
		t.Fatalf("start empty ledger %s: %v", containerName, err)
	}
	started, err := startEmptyLedgerContainer(ctx, cli, containerName, superuserKey)
	if err != nil {
		t.Fatalf("start empty ledger %s: %v", containerName, err)
	}
	removeLedgerAfterTest(t, started.name)
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(ledgerAdminUser, superuserKey),
		Host:     net.JoinHostPort(started.address, ledgerPort),
		Path:     "/" + ledgerDatabase,
		RawQuery: "sslmode=disable",
	}
	if err := waitForLedger(ctx, dsn.String()); err != nil {
		t.Fatalf("start empty ledger %s: %v", containerName, err)
	}
	image, err := serviceImage(ctx, ledgerService)
	if err != nil {
		t.Fatalf("start empty ledger %s: %v", containerName, err)
	}
	slog.InfoContext(ctx, "testenv.ledger.empty_ready", slog.String("container", started.name))
	return EmptyLedgerNode{
		Image:         image,
		DSN:           dsn.String(),
		Address:       started.address,
		MasterAddress: net.JoinHostPort(started.address, ledgerMasterPort),
		Network:       networkName,
	}
}

// refuseExistingContainer returns an error when a container named
// containerName exists on the daemon.
func refuseExistingContainer(ctx context.Context, cli *client.Client, containerName string) error {
	_, err := cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{Size: false})
	if err == nil {
		return fmt.Errorf("a container named %s already exists; remove it before this test starts its own", containerName)
	}
	if !cerrdefs.IsNotFound(err) {
		slog.ErrorContext(ctx, "testenv.ledger.inspect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("inspect container %s: %w", containerName, err)
	}
	return nil
}

// startEmptyLedgerContainer creates and starts the node's container from the
// stack's yugabyte image.
func startEmptyLedgerContainer(ctx context.Context, cli *client.Client, containerName, superuserKey string) (engine, error) {
	image, err := serviceImage(ctx, ledgerService)
	if err != nil {
		return engine{}, err
	}
	platform, err := ledgerPlatform(ctx, cli)
	if err != nil {
		return engine{}, err
	}
	command := append(slices.Clone(ledgerCommand), "--advertise_address="+containerName, "--listen="+containerName)
	return startEngine(ctx, cli, engineSpec{
		kind:     ledgerService,
		image:    image,
		platform: platform,
		cmd:      command,
		env: []string{
			"YSQL_USER=" + ledgerAdminUser,
			"YSQL_PASSWORD=" + superuserKey,
			"YSQL_DB=" + ledgerDatabase,
		},
		name: containerName,
		healthcheck: &container.HealthConfig{
			Test:          []string{"CMD-SHELL", ledgerHealthTest},
			Interval:      ledgerHealthInterval,
			Timeout:       ledgerHealthTimeout,
			StartPeriod:   ledgerHealthStartPeriod,
			StartInterval: 0,
			Retries:       ledgerHealthRetries,
		},
	})
}

// removeLedgerAfterTest removes the node's container when the test ends.
func removeLedgerAfterTest(t *testing.T, containerName string) {
	t.Helper()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		if err := removeContainers(cleanup, []string{containerName}); err != nil {
			t.Errorf("remove empty ledger %s: %v", containerName, err)
		}
	})
}
