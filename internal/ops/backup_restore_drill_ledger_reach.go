// The ledger reader connects to the temporary database that the YugabyteDB
// drill restored. It derives the address from the run-specific scratch
// container without reading the live cluster's connection string. This avoids
// mistaking a check of the running ledger for proof of a successful restore.

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
	// drillLedgerPort selects the scratch yugabyted YSQL port, matching the drill's
	// ysqlsh calls.
	drillLedgerPort = "5433"

	// drillLedgerDefaultNetwork selects Docker's bridge network when the backup
	// configuration does not specify a network. The address lookup uses it too.
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

// scratchLedgerAddress returns the restored database address for this process.
// The scratch container name resolves through Compose's embedded DNS, while
// the drill uses the host network. The function therefore reads the named
// container's endpoint on the drill network instead of using the live cluster
// configuration.
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

// endpointAddress selects the container address on networkName. It prefers
// IPv6 because the production bridge does not provide IPv4. An invalid result
// indicates that the endpoint does not provide a usable address.
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

// scratchLedgerDSN builds the ledger connection string from the supplied
// scratch address, run-specific role, and temporary password. The drill
// supplies these values from the scratch restore instead of the live ledger.
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
