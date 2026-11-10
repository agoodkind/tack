package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// selfContainer returns the full ID of the current process's container or an
// empty string when the local daemon does not recognize its hostname.
func selfContainer(ctx context.Context, cli *client.Client) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("read this host's name: %w", err)
	}
	self, err := macObserve(ctx, func(observation context.Context) (client.ContainerInspectResult, error) {
		return cli.ContainerInspect(observation, hostname, client.ContainerInspectOptions{})
	})
	if cerrdefs.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "testenv.self.inspect_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("inspect container %s: %w", hostname, err)
	}
	configuration := self.Container.Config
	if configuration == nil {
		return "", fmt.Errorf("inspect container %s omitted configuration", hostname)
	}
	marked := configuration.Labels[macRunnerLabel] != "" || configuration.Labels[macRunnerLabel+".native"] != ""
	if configuration.Hostname != hostname {
		if marked {
			return "", fmt.Errorf("marked native runner %s does not match current hostname %s", self.Container.ID, hostname)
		}
		return "", nil
	}
	return self.Container.ID, nil
}
