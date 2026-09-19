// backup_restore_drill_yb_host.go names the address the drill's scratch
// yugabyted advertises and listens on, which every client the drill runs
// inside that container dials. The address is separate from the container
// name because yugabyted validates its advertise address against a DNS-name
// pattern that backtracks exponentially over a name it rejects, and it rejects
// every name that does not end in two letters. The container name ends in the
// run id, and the run id ends in the drill's pid, so the check alone took 10s
// natively for a five-digit pid and 39s for a seven-digit one, doubling with
// each further character and running about 26 times slower under emulation
// (TACK-527). The container name keeps the run id at its end, which is what the
// orphan sweep reads it back from.

package ops

import "github.com/moby/moby/api/types/network"

// ybScratchHostSuffix ends the scratch host name in letters joined directly to
// the run id, which is the shape the pattern accepts at its first attempt. A
// hyphen before the letters would not do: the pattern forbids a label that
// ends in one, so "-host" would be rejected the slow way.
const ybScratchHostSuffix = "host"

// ybScratchHost is the scratch yugabyted's host name for the container named
// containerName. It stays unique per run because the container name is.
func ybScratchHost(containerName string) string {
	return containerName + ybScratchHostSuffix
}

// ybScratchNetworking joins the drill network the way [netMode] does, adding
// the host name as a network alias so Docker's embedded DNS answers for it as
// it answers for the container name. The container's hostname is set to the
// same name, which answers for it from /etc/hosts, including on the default
// bridge, where Docker refuses aliases and the network name is empty.
func ybScratchNetworking(networkName, host string) *network.NetworkingConfig {
	networking := netMode(networkName)
	if networking == nil {
		return nil
	}
	networking.EndpointsConfig[networkName].Aliases = []string{host}
	return networking
}
