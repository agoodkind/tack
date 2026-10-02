package testenv

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	// ledgerClusterPoolBase is 10.200.0.0, the first address of the /14 that
	// every cluster network takes one /24 from.
	ledgerClusterPoolBase uint32 = 10<<24 | 200<<16
	// ledgerClusterSubnets is the number of /24 networks in the /14.
	ledgerClusterSubnets = 1 << 10
	// ledgerClusterSubnetBits is the prefix length of one cluster network.
	ledgerClusterSubnetBits = 24
	// ledgerClusterDynamicHost and ledgerClusterDynamicBits are the upper half
	// of the /24, where the daemon allocates addresses such as the yb-admin
	// one-shot's. The nodes take fixed addresses in the lower half.
	ledgerClusterDynamicHost = 128
	ledgerClusterDynamicBits = 25
	// ledgerClusterFirstNodeHost is the host number of the first node.
	ledgerClusterFirstNodeHost = 10
	// ledgerClusterNetworkAttempts bounds the random subnets tried before the
	// cluster gives up on a pool that other networks fill.
	ledgerClusterNetworkAttempts = 8
	// ledgerClusterNetworkKind is the kind part of the generated network name.
	ledgerClusterNetworkKind = "ledger-network"
)

// ledgerClusterSubnet returns the /24 at index inside the pool.
func ledgerClusterSubnet(index uint32) netip.Prefix {
	var octets [4]byte
	binary.BigEndian.PutUint32(octets[:], ledgerClusterPoolBase+index<<8)
	return netip.PrefixFrom(netip.AddrFrom4(octets), ledgerClusterSubnetBits)
}

// ledgerClusterHost returns the address with host number host in subnet.
func ledgerClusterHost(subnet netip.Prefix, host uint32) netip.Addr {
	octets := subnet.Addr().As4()
	binary.BigEndian.PutUint32(octets[:], binary.BigEndian.Uint32(octets[:])+host)
	return netip.AddrFrom4(octets)
}

// randomLedgerClusterSubnet returns a random /24 of the pool.
func randomLedgerClusterSubnet() netip.Prefix {
	var random [2]byte
	_, _ = rand.Read(random[:])
	return ledgerClusterSubnet(uint32(binary.BigEndian.Uint16(random[:])) % ledgerClusterSubnets)
}

// createLedgerClusterNetwork creates an IPv4 bridge network for one cluster
// on a random /24 of the pool and returns its name and subnet. A subnet that
// overlaps another network is retried with a new random /24, at most
// ledgerClusterNetworkAttempts times.
func createLedgerClusterNetwork(ctx context.Context, cli *client.Client) (string, netip.Prefix, error) {
	name, err := generatedEngineName(ctx, ledgerClusterNetworkKind)
	if err != nil {
		return "", netip.Prefix{}, err
	}
	created := netip.Prefix{}
	for attempt := 1; attempt <= ledgerClusterNetworkAttempts && !created.IsValid(); attempt++ {
		subnet := randomLedgerClusterSubnet()
		_, err := cli.NetworkCreate(ctx, name, ledgerClusterNetworkOptions(subnet))
		switch {
		case err == nil:
			created = subnet
		case strings.Contains(strings.ToLower(err.Error()), "overlap"):
			slog.DebugContext(ctx, "testenv.ledger_cluster.subnet_taken",
				slog.String("subnet", subnet.String()), slog.Int("attempt", attempt))
		default:
			slog.ErrorContext(ctx, "testenv.ledger_cluster.network_create_failed", slog.String("err", err.Error()))
			return "", netip.Prefix{}, fmt.Errorf("create network %s on %s: %w", name, subnet, err)
		}
	}
	if !created.IsValid() {
		return "", netip.Prefix{}, fmt.Errorf("create network %s: each of %d random /24 subnets overlapped another network",
			name, ledgerClusterNetworkAttempts)
	}
	slog.InfoContext(ctx, "testenv.ledger_cluster.network_created",
		slog.String("network", name), slog.String("subnet", created.String()))
	return name, created, nil
}

// ledgerClusterNetworkOptions configures a managed IPv4 bridge on subnet with
// the daemon's dynamic range in the upper half.
func ledgerClusterNetworkOptions(subnet netip.Prefix) client.NetworkCreateOptions {
	enableIPv4, enableIPv6 := true, false
	dynamic := netip.PrefixFrom(ledgerClusterHost(subnet, ledgerClusterDynamicHost), ledgerClusterDynamicBits)
	options := client.NetworkCreateOptions{}
	options.Driver = "bridge"
	options.EnableIPv4, options.EnableIPv6 = &enableIPv4, &enableIPv6
	options.IPAM = &network.IPAM{
		Driver: "", Options: nil,
		Config: []network.IPAMConfig{{Subnet: subnet, IPRange: dynamic, Gateway: netip.Addr{}, AuxAddress: nil}},
	}
	options.Labels = map[string]string{managedLabel: "true", processLabel: strconv.Itoa(os.Getpid())}
	return options
}

// removeLedgerClusterNetwork detaches the container with ID selfID from
// network name when selfID is set, then removes the network. A network or an
// endpoint already gone counts as removed.
func removeLedgerClusterNetwork(ctx context.Context, cli *client.Client, name, selfID string) error {
	var failures []error
	if selfID != "" {
		_, err := cli.NetworkDisconnect(ctx, name, client.NetworkDisconnectOptions{Container: selfID, Force: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			failures = append(failures, fmt.Errorf("detach container %s from network %s: %w", selfID, name, err))
		}
	}
	_, err := cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		failures = append(failures, fmt.Errorf("remove network %s: %w", name, err))
	}
	if err := errors.Join(failures...); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger_cluster.network_remove_failed", slog.String("err", err.Error()))
		return err
	}
	return nil
}
