// Scratch yugabyted uses a separate hostname because its advertise-address
// validation rejects names ending in digits with exponential backtracking.
// Validation took 10 seconds for a five-digit PID and 39 seconds for a
// seven-digit PID natively, and about 26 times longer under emulation
// (TACK-527). Container names retain the run ID for orphan cleanup.

package ops

import "github.com/moby/moby/api/types/network"

// ybScratchHostSuffix appends letters directly to the run ID to satisfy
// yugabyted's hostname pattern. A hyphen before the suffix would make the
// preceding label invalid and trigger slow validation.
const ybScratchHostSuffix = "host"

// ybScratchHost returns a unique scratch hostname for the container.
func ybScratchHost(containerName string) string {
	return containerName + ybScratchHostSuffix
}

// ybScratchNetworking adds the scratch hostname as a Docker network alias.
// The container also uses this hostname in /etc/hosts. The default bridge
// does not support aliases; netMode returns nil for its empty network name.
func ybScratchNetworking(networkName, host string) *network.NetworkingConfig {
	networking := netMode(networkName)
	if networking == nil {
		return nil
	}

	networking.EndpointsConfig[networkName].Aliases = []string{host}

	return networking
}
