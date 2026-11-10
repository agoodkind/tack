package testenv

import (
	"net/netip"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/network"
)

func macRunnerIPAM(configuration types.IPAMConfig) (*network.IPAM, error) {
	ipam := &network.IPAM{Driver: configuration.Driver, Options: configuration.Options}
	for _, pool := range configuration.Config {
		converted, err := macRunnerIPAMPool(pool)
		if err != nil {
			return nil, err
		}
		ipam.Config = append(ipam.Config, converted)
	}
	return ipam, nil
}

func macRunnerIPAMPool(pool *types.IPAMPool) (network.IPAMConfig, error) {
	var converted network.IPAMConfig
	for _, item := range []struct {
		value  string
		target *netip.Prefix
	}{{pool.Subnet, &converted.Subnet}, {pool.IPRange, &converted.IPRange}} {
		value, target := item.value, item.target
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return converted, macRunnerFailure("parse default-network IPAM prefix "+value, err)
		}
		*target = prefix
	}
	if pool.Gateway != "" {
		gateway, err := netip.ParseAddr(pool.Gateway)
		if err != nil {
			return converted, macRunnerFailure("parse default-network gateway "+pool.Gateway, err)
		}
		converted.Gateway = gateway
	}
	converted.AuxAddress = make(map[string]netip.Addr)
	for name, value := range pool.AuxiliaryAddresses {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return converted, macRunnerFailure("parse default-network auxiliary address "+name, err)
		}
		converted.AuxAddress[name] = address
	}
	return converted, nil
}
