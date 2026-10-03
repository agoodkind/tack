package integration

import (
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// deployFixture is a disposable daemon with a registry running inside it. The
// daemon is the DOCKER_HOST of the test.
type deployFixture struct {
	ledgerDSN    string
	cli          *client.Client
	registryHost string
	registry     localRegistry
	tag          string
}

// startDeployFixture starts the daemon with start, points DOCKER_HOST at it,
// refuses a daemon that already has the two deploy containers, and starts the
// registry. Every image the test pushes uses the fixture tag.
func startDeployFixture(t *testing.T, start func(*testing.T) (string, *client.Client)) deployFixture {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	testenv.RequireDocker(t)
	endpoint, cli := start(t)
	t.Setenv("DOCKER_HOST", endpoint)
	for _, name := range []string{"tack-app-1", "tack-audit-consumer-1"} {
		if _, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{}); err == nil {
			t.Fatalf("container %s exists on this daemon; the test refuses to replace it", name)
		}
	}
	daemonURL, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse the daemon endpoint %s: %v", endpoint, err)
	}
	registry, hostPort := startDeployRegistry(t, cli, daemonURL.Hostname())
	return deployFixture{
		ledgerDSN: ledgerDSN, cli: cli, registryHost: "localhost:" + hostPort,
		registry: registry, tag: "m13-" + uuid.NewString()[:8],
	}
}

// pushIndexes pushes a multi-platform index image for tack-server and for
// tack-audit-consumer and returns the two index digests.
func (f deployFixture) pushIndexes(t *testing.T) (string, string) {
	t.Helper()
	serverDigest := f.registry.pushMultiPlatformIndex(t, "tack-server", f.tag, "server "+f.tag)
	consumerDigest := f.registry.pushMultiPlatformIndex(t, "tack-audit-consumer", f.tag, "consumer "+f.tag)
	return serverDigest, consumerDigest
}

// createContainers pulls the pushed tack-server and tack-audit-consumer images
// by tag and creates, without starting, the two containers deploy verify reads.
func (f deployFixture) createContainers(t *testing.T) {
	t.Helper()
	createFromIndex(t, f.cli, "tack-app-1", f.registryHost+"/tack-server:"+f.tag)
	createFromIndex(t, f.cli, "tack-audit-consumer-1", f.registryHost+"/tack-audit-consumer:"+f.tag)
}

// verify runs the audited ops deploy verify command against the fixture with
// the two digest flags.
func (f deployFixture) verify(t *testing.T, serverDigest, consumerDigest string) (string, error) {
	t.Helper()
	cfg := &config.Config{DeployRegistry: f.registryHost, DeployImageTag: f.tag}
	return runOpsDeployVerify(t, cfg, f.ledgerDSN, serverDigest, consumerDigest)
}
