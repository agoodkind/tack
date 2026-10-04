package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestDeployVerifyRefusesASingleManifestImage pushes a single-platform image
// with no index for tack-server and for tack-audit-consumer to a registry inside
// a containerd image store daemon. The audited command refuses the image
// descriptor, which is a manifest, even when the given digest is that manifest
// digest.
func TestDeployVerifyRefusesASingleManifestImage(t *testing.T) {
	fixture := startDeployFixture(t, testenv.ContainerdDocker)
	requireContainerdImageStore(t, fixture.cli)
	serverDigest := fixture.registry.pushSingleManifestImage(t, "tack-server", fixture.tag, "server "+fixture.tag)
	consumerDigest := fixture.registry.pushSingleManifestImage(t, "tack-audit-consumer", fixture.tag, "consumer "+fixture.tag)
	fixture.createContainers(t)

	logDeployImageReads(t, fixture.cli, "tack-app-1")

	output, err := fixture.verify(t, serverDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "is not an image index") || !strings.Contains(err.Error(), "tack-app-1") {
		t.Fatalf("deploy verify with single-manifest digests = %v, want a media type refusal for tack-app-1\n%s", err, output)
	}
	t.Logf("single manifest image refused: %v", err)
}
