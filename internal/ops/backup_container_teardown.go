package ops

import (
	"context"
	"log/slog"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func cleanupOneShot(ctx context.Context, cli *client.Client, identifier string) {
	if ownsBackupOperationLock(ctx) {
		awaitBackupContainerRemoval(ctx, cli, identifier)
		return
	}
	teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	removeContainerForce(teardown, cli, identifier)
}

// awaitBackupContainerRemoval keeps the backup lock until Docker confirms that
// the container cannot continue reading or deleting backups after cancellation.
func awaitBackupContainerRemoval(ctx context.Context, cli *client.Client, name string) {
	for {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_, err := cli.ContainerRemove(cleanup, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		cancel()
		if err == nil || cerrdefs.IsNotFound(err) {
			return
		}
		slog.ErrorContext(context.WithoutCancel(ctx), "backup.container.removal_retry", slog.String("container", name), slog.String("err", err.Error()))
		// A failed removal cannot release the shared backup lock safely.
		timer := time.NewTimer(time.Second)
		<-timer.C
	}
}
