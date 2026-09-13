package ops

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
)

// newLocalDockerClient pins the daemon-bound tests to the local daemon by
// overriding any DOCKER_HOST inherited from the environment, so a test never
// reaches a remote daemon by accident.
func newLocalDockerClient(_ context.Context) (*client.Client, error) {
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		return nil, fmt.Errorf("local docker client: %w", err)
	}
	return cli, nil
}
