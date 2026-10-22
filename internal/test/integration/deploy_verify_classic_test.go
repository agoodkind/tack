package integration

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

// TestDeployVerifyRefusesTheClassicImageStore runs the same flow as the index
// digest test against a docker:dind daemon with the classic overlay2 image
// store. The daemon reports no containerd driver-type. The audited command
// refuses the store although the given digests are the pushed index digests.
func TestDeployVerifyRefusesTheClassicImageStore(t *testing.T) {
	fixture := startDeployFixture(t, testenv.ClassicDocker)
	requireClassicImageStore(t, fixture.cli)
	serverDigest, consumerDigest := fixture.pushIndexes(t)
	fixture.createContainers(t)

	logDeployImageReads(t, fixture.cli, "tack-app-1")

	output, err := fixture.verify(t, serverDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "unsupported image store") || !strings.Contains(err.Error(), `driver-type "`) {
		t.Fatalf("deploy verify on the classic image store = %v, want unsupported image store with the driver-type\n%s", err, output)
	}
	if strings.Contains(err.Error(), `driver-type "`+ops.ContainerdSnapshotterDriverType+`"`) {
		t.Fatalf("the refusal reports the containerd driver-type: %v", err)
	}
	t.Logf("classic image store refused: %v", err)
}
