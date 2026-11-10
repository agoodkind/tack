package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/test/integration"
)

// TestDeployVerifyChecksOnlyTheAppServiceOnAnAppOnlyGuest starts a disposable
// docker:dind daemon with the containerd image store and creates only the app
// container, the shape of an application-only guest. The audited command
// without --service refuses the guest because it has no audit-consumer
// replica. With --service app, the command passes with the pushed tack-server
// index digest, refuses the other image's index digest, and refuses
// --tack-audit-consumer-digest.
func TestDeployVerifyChecksOnlyTheAppServiceOnAnAppOnlyGuest(t *testing.T) {
	fixture := integration.StartContainerdDeployFixture(t)
	serverDigest, consumerDigest := integration.PushDeployIndexes(t, fixture)
	integration.CreateDeployAppContainer(t, fixture)
	operatorID := uuid.Must(uuid.NewV7()).String()

	output, err := integration.VerifyDeployAs(t, fixture, operatorID, serverDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "service audit-consumer") {
		t.Fatalf("deploy verify of both services on an app-only guest = %v, want a refusal for the missing audit-consumer replica\n%s", err, output)
	}
	t.Logf("both services refused: %v", err)

	output, err = integration.VerifyDeployArgsAs(t, fixture, operatorID, "--service", "app", "--tack-server-digest", serverDigest)
	if err != nil {
		t.Fatalf("deploy verify --service app with the pushed tack-server index digest: %v\n%s", err, output)
	}
	if !strings.Contains(output, "tack-app-1 runs index "+serverDigest) || strings.Contains(output, "audit-consumer") {
		t.Fatalf("deploy verify --service app output = %q, want only the tack-app-1 line with the given digest", output)
	}
	t.Logf("app service verified: %s", strings.TrimSpace(output))

	output, err = integration.VerifyDeployArgsAs(t, fixture, operatorID, "--service", "app", "--tack-server-digest", consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "descriptor digest differs") || !strings.Contains(err.Error(), "tack-app-1") {
		t.Fatalf("deploy verify --service app with the other index digest = %v, want a refusal for tack-app-1\n%s", err, output)
	}

	output, err = integration.VerifyDeployArgsAs(t, fixture, operatorID,
		"--service", "app", "--tack-server-digest", serverDigest, "--tack-audit-consumer-digest", consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "refuses --tack-audit-consumer-digest") {
		t.Fatalf("deploy verify --service app with an audit-consumer digest = %v, want a refusal of that flag\n%s", err, output)
	}
}
