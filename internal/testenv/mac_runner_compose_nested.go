package testenv

import (
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"
)

type macRunnerUnsupportedOption struct {
	field   string
	present bool
}

func validateMacRunnerNestedCompose(service types.ServiceConfig) error {
	for _, volume := range service.Volumes {
		options := []macRunnerUnsupportedOption{
			{field: "consistency", present: volume.Consistency != ""},
			{field: "tmpfs", present: volume.Tmpfs != nil},
			{field: "image", present: volume.Image != nil},
		}
		if volume.Bind != nil {
			options = append(options,
				macRunnerUnsupportedOption{field: "bind.selinux", present: volume.Bind.SELinux != ""},
				macRunnerUnsupportedOption{field: "bind.propagation", present: volume.Bind.Propagation != ""},
				macRunnerUnsupportedOption{field: "bind.recursive", present: volume.Bind.Recursive != ""},
			)
		}
		if volume.Volume != nil {
			options = append(options,
				macRunnerUnsupportedOption{field: "volume.subpath", present: volume.Volume.Subpath != ""},
				macRunnerUnsupportedOption{field: "volume.nocopy", present: volume.Volume.NoCopy},
				macRunnerUnsupportedOption{field: "volume.labels", present: len(volume.Volume.Labels) != 0},
			)
		}
		if err := refuseMacRunnerOptions("volume "+volume.Target, options); err != nil {
			return err
		}
	}
	for name, endpoint := range service.Networks {
		if endpoint == nil {
			continue
		}
		options := []macRunnerUnsupportedOption{
			{field: "aliases", present: len(endpoint.Aliases) != 0},
			{field: "driver_opts", present: len(endpoint.DriverOpts) != 0},
			{field: "gw_priority", present: endpoint.GatewayPriority != 0},
			{field: "interface_name", present: endpoint.InterfaceName != ""},
			{field: "ipv4_address", present: endpoint.Ipv4Address != ""},
			{field: "ipv6_address", present: endpoint.Ipv6Address != ""},
			{field: "link_local_ips", present: len(endpoint.LinkLocalIPs) != 0},
			{field: "mac_address", present: endpoint.MacAddress != ""},
			{field: "priority", present: endpoint.Priority != 0},
			{field: "use_service_name_alias", present: endpoint.UseServiceNameAlias != nil},
		}
		if err := refuseMacRunnerOptions("network endpoint "+name, options); err != nil {
			return err
		}
	}
	return nil
}

func refuseMacRunnerOptions(subject string, options []macRunnerUnsupportedOption) error {
	for _, option := range options {
		if option.present {
			return fmt.Errorf("unsupported tests %s field %s", subject, option.field)
		}
	}
	return nil
}
