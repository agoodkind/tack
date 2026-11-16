// Restored-ledger readers use only the run's scratch container address and
// credentials. They never read the live connection string or verify the live
// ledger as a substitute for the restored database.

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
	// drillLedgerPort specifies the scratch YSQL and ysqlsh port.
	drillLedgerPort = "5433"
	// drillLedgerDefaultNetwork selects Docker's default bridge when backup
	// configuration does not specify a network.
	drillLedgerDefaultNetwork = "bridge"
)

// openRestoredLedgerReader opens the read pool against the scratch address.
func openRestoredLedgerReader(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
	database string,
	roleName string,
) (*audit.Reader, error) {
	logger := telemetry.L(ctx)
	address, err := scratchLedgerAddress(ctx, r, containerName)
	if err != nil {
		return nil, err
	}

	reader, err := audit.NewReader(ctx, scratchLedgerDSN(address, database, roleName, r.YBPass))
	if err != nil {
		wrapped := fmt.Errorf(
			"open a ledger reader on the restored database at %s: %w",
			address,
			err,
		)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	return reader, nil
}

// scratchLedgerAddress inspects the run-specific container's network endpoint.
// The host-networked drill cannot use Compose's embedded container-name DNS.
// Inspecting the scratch container avoids substituting a live ledger address.
func scratchLedgerAddress(
	ctx context.Context,
	r *restoreDrillCtx,
	containerName string,
) (netip.Addr, error) {
	logger := telemetry.L(ctx)
	inspected, err := r.Cli.ContainerInspect(
		ctx,
		containerName,
		client.ContainerInspectOptions{Size: false},
	)
	if err != nil {
		wrapped := fmt.Errorf("inspect scratch yugabyted %s: %w", containerName, err)
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", wrapped.Error()),
		)

		return netip.Addr{}, wrapped
	}

	networkName := r.Cfg.BackupFDBNetwork
	if networkName == "" {
		networkName = drillLedgerDefaultNetwork
	}

	address := endpointAddress(inspected, networkName)
	if !address.IsValid() {
		wrapped := scratchLedgerAddressMissingError{Container: containerName, Network: networkName}
		logger.ErrorContext(
			ctx,
			"backup.restore_drill.ledger_chain.failed",
			slog.String("err", wrapped.Error()),
		)

		return netip.Addr{}, wrapped
	}

	logger.InfoContext(
		ctx,
		"backup.restore_drill.ledger_chain.address",
		slog.String("container", containerName),
		slog.String("address", address.String()),
	)

	return address, nil
}

type scratchLedgerAddressMissingError struct {
	Container string
	Network   string
}

func (failure scratchLedgerAddressMissingError) Error() string {
	return fmt.Sprintf(
		"scratch yugabyted %s does not provide an address on network %s; the drill cannot read the restored ledger",
		failure.Container,
		failure.Network,
	)
}

// endpointAddress prefers IPv6 for the IPv6-only production bridge. An invalid
// result indicates an absent usable endpoint on the requested network.
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

// scratchLedgerDSN combines only the scratch address, run-specific role, and
// temporary password rather than a live-ledger connection string.
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

// ysqlshRoleArgs builds ysqlsh arguments to connect as roleName.
func ysqlshRoleArgs(host, database, roleName, sql string) []string {
	return []string{
		"ysqlsh",
		"-h",
		host,
		"-p",
		drillLedgerPort,
		"-U",
		roleName,
		"-d",
		database,
		"-tAc",
		sql,
	}
}
