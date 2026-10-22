package testenv

import (
	"net/netip"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// engineNetwork returns the network that spec's engine joins.
func engineNetwork(spec engineSpec) string {
	if spec.attachNetwork != "" {
		return spec.attachNetwork
	}
	return networkName
}

// engineNetworking returns the host and networking configuration of spec's
// container. A spec with networkOf shares that container's network stack.
// Any other spec gets one endpoint on engineNetwork with spec's fixed
// address, and spec's extra hosts entries.
func engineNetworking(spec engineSpec) (*container.HostConfig, *network.NetworkingConfig) {
	hostConfig := &container.HostConfig{}
	hostConfig.ExtraHosts = spec.extraHosts
	hostConfig.Privileged = spec.privileged
	if spec.networkOf != "" {
		hostConfig.NetworkMode = container.NetworkMode("container:" + spec.networkOf)
		return hostConfig, &network.NetworkingConfig{EndpointsConfig: nil}
	}
	endpoint := &network.EndpointSettings{}
	if spec.ipv4Address.IsValid() {
		endpoint.IPAMConfig = &network.EndpointIPAMConfig{
			IPv4Address: spec.ipv4Address, IPv6Address: netip.Addr{}, LinkLocalIPs: nil,
		}
	}
	return hostConfig, &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{engineNetwork(spec): endpoint},
	}
}
