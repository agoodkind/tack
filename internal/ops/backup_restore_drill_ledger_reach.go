// backup_restore_drill_ledger_reach.go finds and opens the throwaway database
// the yugabyte leg just restored. Everything here is scoped to the run: the
// address comes from the run-scoped scratch container, and the live cluster's
// connection string is never read, so this leg cannot verify the running
// ledger by mistake and report that as a successful restore.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// drillLedgerPort is the YSQL port the scratch yugabyted serves on, the
	// same port the leg's ysqlsh calls use.
	drillLedgerPort = "5433"

	// drillLedgerDefaultNetwork is the network a container joins when the
	// backup config names none, which is what the address lookup falls back to.
	drillLedgerDefaultNetwork = "bridge"
)

// openRestoredLedgerReader opens the ledger read pool against the scratch
// container's own address.
func openRestoredLedgerReader(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName, database, roleName string,
) (*audit.Reader, error) {
	logger := telemetry.L(ctx)
	address, err := scratchLedgerAddress(ctx, r, containerName)
	if err != nil {
		return nil, err
	}
	reader, err := audit.NewReader(ctx, scratchLedgerDSN(address, database, roleName, r.YBPass))
	if err != nil {
		wrapped := fmt.Errorf("open a ledger reader on the restored database at %s: %w", address, err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return reader, nil
}

// scratchLedgerAddress returns the address this process dials the restored
// database on. The scratch container's name resolves only through the compose
// network's embedded DNS and the drill runs on the host's network stack, so
// the address comes from the container's own endpoint on the drill network.
// Reading it from the run-scoped container by name is what keeps the leg
// pointed at the restore: no value here can name the live cluster.
func scratchLedgerAddress(ctx context.Context, r *restoreDrillCtx, containerName string) (netip.Addr, error) {
	logger := telemetry.L(ctx)
	inspected, err := r.Cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{Size: false})
	if err != nil {
		wrapped := fmt.Errorf("inspect scratch yugabyted %s: %w", containerName, err)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", wrapped.Error()))
		return netip.Addr{}, wrapped
	}
	networkName := r.Cfg.BackupFDBNetwork
	if networkName == "" {
		networkName = drillLedgerDefaultNetwork
	}
	address := endpointAddress(inspected, networkName)
	if !address.IsValid() {
		wrapped := fmt.Errorf(
			"scratch yugabyted %s has no address on network %s, so the restored ledger cannot be read",
			containerName, networkName)
		logger.ErrorContext(ctx, "backup.restore_drill.ledger_chain.failed", slog.String("err", wrapped.Error()))
		return netip.Addr{}, wrapped
	}
	logger.InfoContext(ctx, "backup.restore_drill.ledger_chain.address",
		slog.String("container", containerName), slog.String("address", address.String()))
	return address, nil
}

// endpointAddress picks the container's address on the named network,
// preferring its IPv6 address because the production bridge carries no IPv4.
// An invalid result means the container has no usable address there.
func endpointAddress(inspected client.ContainerInspectResult, networkName string) netip.Addr {
	settings := inspected.Container.NetworkSettings
	if settings == nil {
		return netip.Addr{}
	}
	endpoint, found := settings.Networks[networkName]
	if !found || endpoint == nil {
		return netip.Addr{}
	}
	if endpoint.GlobalIPv6Address.IsValid() {
		return endpoint.GlobalIPv6Address
	}
	return endpoint.IPAddress
}

// scratchLedgerDSN is the only connection string this leg builds. It names the
// scratch container's address, the run-scoped role, and the run's throwaway
// password, so nothing in this path can resolve to the live ledger.
func scratchLedgerDSN(address netip.Addr, database, roleName, password string) string {
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(roleName, password),
		Host:     net.JoinHostPort(address.String(), drillLedgerPort),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return dsn.String()
}

// ysqlshRoleArgs builds a ysqlsh command vector that connects as roleName.
func ysqlshRoleArgs(host, database, roleName, sql string) []string {
	return []string{"ysqlsh", "-h", host, "-p", drillLedgerPort, "-U", roleName, "-d", database, "-tAc", sql}
}
