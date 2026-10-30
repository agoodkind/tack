package testenv

import (
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type macRunnerObservationResult interface {
	client.ImageInspectResult | client.PingResult | client.SystemInfoResult | client.ServerVersionResult |
		client.ContainerListResult | client.NetworkListResult | client.VolumeListResult |
		client.NetworkCreateResult | client.ContainerCreateResult | client.ContainerInspectResult |
		client.ContainerRemoveResult | client.ContainerStopResult | client.VolumeInspectResult |
		client.VolumeRemoveResult | client.NetworkInspectResult | client.NetworkRemoveResult |
		client.ContainerStartResult | client.ContainerTopResult
}

type macRunnerEvidenceValue interface {
	macRunnerDaemon | macRunnerPinnedImage | macRunnerEnvironment | client.ContainerCreateOptions |
		container.InspectResponse | macRunnerCreateSummary | macRunnerRefusal | []macRunnerRefusal |
		macRunnerSummary
}

type macRunnerPinnedImage struct {
	Image              client.ImageInspectResult `json:"image"`
	DaemonArchitecture string                    `json:"daemon_architecture"`
}

type macRunnerEnvironment struct {
	ImageDefaults     []string `json:"image_defaults"`
	PreparedOverrides []string `json:"prepared_overrides"`
}

type macRunnerCreateSummary struct {
	Attempts       int    `json:"attempts"`
	AcceptedID     string `json:"accepted_id,omitempty"`
	SocketRefusals int    `json:"socket_refusals"`
}
