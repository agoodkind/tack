package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestDeployVerifyAcceptsADockerManifestList pushes each image as a Docker
// manifest list, the second multi-platform media type, to a registry inside a
// containerd image store daemon. The audited command passes with the pushed
// list digests.
func TestDeployVerifyAcceptsADockerManifestList(t *testing.T) {
	fixture := startDeployFixture(t, testenv.ContainerdDocker)
	requireContainerdImageStore(t, fixture.cli)
	serverDigest := fixture.registry.pushMultiPlatformList(t, "tack-server", fixture.tag, "server "+fixture.tag, dockerManifestListMediaType)
	consumerDigest := fixture.registry.pushMultiPlatformList(t, "tack-audit-consumer", fixture.tag, "consumer "+fixture.tag, dockerManifestListMediaType)
	fixture.createContainers(t)

	logDeployImageReads(t, fixture.cli, "tack-app-1")

	output, err := fixture.verify(t, serverDigest, consumerDigest)
	if err != nil {
		t.Fatalf("deploy verify with the pushed manifest list digests: %v\n%s", err, output)
	}
	t.Logf("manifest list digests accepted: %s", strings.TrimSpace(output))
}
