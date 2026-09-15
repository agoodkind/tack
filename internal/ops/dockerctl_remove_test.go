// dockerctl_remove_test.go proves scratch container teardown takes the
// container's anonymous volumes with it and leaves named volumes alone. It
// needs a Docker daemon, so it is gated the same way the exec deadline test is
// and skips in the unit suite.

package ops

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// TestRemoveContainerForceRemovesAnonymousVolumes is the leak TACK-498 found:
// each scratch engine's image declares a VOLUME, and teardown that removed
// only the container left one volume behind per drill. The container below
// gets an anonymous volume the way an image VOLUME creates one and a named
// volume the way the live stack mounts one; teardown must remove the first
// and keep the second.
func TestRemoveContainerForceRemovesAnonymousVolumes(t *testing.T) {
	if os.Getenv("DEPLOY_TEST_INTEGRATION") != "1" {
		t.Skip("DEPLOY_TEST_INTEGRATION!=1; skipping daemon-bound integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cli, err := newLocalDockerClient(ctx)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	const image = "alpine:3"
	if err := ensureImage(ctx, cli, nopLogger(), image); err != nil {
		t.Fatalf("ensure %s: %v", image, err)
	}
	named := "tack-remove-test-named-" + time.Now().UTC().Format("20060102T150405")
	if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: named}); err != nil {
		t.Fatalf("create named volume: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.VolumeRemove(context.WithoutCancel(ctx), named, client.VolumeRemoveOptions{Force: true})
	})
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, Cmd: []string{"true"}, Volumes: map[string]struct{}{"/scratch": {}}},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: named, Target: "/kept"}},
		},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	inspected, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect container: %v", err)
	}
	var anonymous string
	for _, point := range inspected.Container.Mounts {
		if point.Destination == "/scratch" {
			anonymous = point.Name
		}
	}
	if anonymous == "" {
		t.Fatal("the container must carry an anonymous volume at /scratch before teardown")
	}

	removeContainerForce(ctx, cli, created.ID)

	if _, err := cli.VolumeInspect(ctx, anonymous, client.VolumeInspectOptions{}); err == nil {
		t.Fatalf("anonymous volume %s outlived its container", anonymous)
	}
	if _, err := cli.VolumeInspect(ctx, named, client.VolumeInspectOptions{}); err != nil {
		t.Fatalf("named volume %s must survive teardown: %v", named, err)
	}
}
