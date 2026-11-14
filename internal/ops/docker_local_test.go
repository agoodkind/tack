package ops

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
)

// newLocalDockerClient connects tests to the local daemon. It overrides
// DOCKER_HOST to prevent tests from using a remote daemon.
func newLocalDockerClient(_ context.Context) (*client.Client, error) {
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		return nil, fmt.Errorf("local docker client: %w", err)
	}

	return cli, nil
}
