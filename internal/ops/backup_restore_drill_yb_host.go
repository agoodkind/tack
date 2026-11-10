// The scratch yugabyted uses a separate advertised host name for clients
// inside its container. Its DNS-name validator rejects names without two final
// letters and backtracks exponentially on rejected names. Run IDs end in the
// process ID, making the container name unsuitable for that validator.
//
// TACK-527 recorded these historical address-check measurements:
//
//   - A five-digit process ID required 10 seconds natively.
//   - A seven-digit process ID required 39 seconds natively.
//   - Each extra character doubled the delay.
//   - Emulation ran about 26 times slower than native execution.
//
// The container name still ends in the run ID because the orphan sweep
// extracts the ID from that suffix.

package ops

import "github.com/moby/moby/api/types/network"

// ybScratchHostSuffix appends letters directly after the run ID. The validator
// accepts that form on its first attempt. An intervening hyphen would end the
// preceding matched label with a hyphen. The pattern rejects the "-host" form
// and performs the slow check.
const ybScratchHostSuffix = "host"

// ybScratchHost returns the host name for the scratch container. The unique
// container name makes the host name unique per run.
func ybScratchHost(containerName string) string {
	return containerName + ybScratchHostSuffix
}

// ybScratchNetworking uses [netMode] and adds the host as a network alias.
// Docker's embedded DNS then resolves the host as well as the container name.
// The container also uses that hostname in /etc/hosts. That local resolution
// works on the default bridge, where the empty network name prevents aliases.
func ybScratchNetworking(networkName, host string) *network.NetworkingConfig {
	networking := netMode(networkName)
	if networking == nil {
		return nil
	}
	networking.EndpointsConfig[networkName].Aliases = []string{host}
	return networking
}
