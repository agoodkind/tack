package integration

import (
	"bytes"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// deployRegistryImage serves the registry HTTP API for the test. The
	// digest is the Docker Hub index of registry:3 read on 2026-10-02.
	deployRegistryImage = "registry:3@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
	// deployTestenvNetwork is the network testenv attaches the test process to.
	deployTestenvNetwork = "tack-testenv"
	// deployTestenvLabel marks containers testenv removes on release.
	deployTestenvLabel = "io.goodkind.tack.testenv"
)

// TestDeployVerifyComparesTheGivenIndexDigest pushes a multi-platform index
// image for tack-server and tack-audit-consumer to a local registry, pulls
// each by tag, and creates (without starting) the two containers ops deploy
// verify reads. The audited command passes with the pushed index digests and
// fails when the tack-server digest is the other image's index digest.
func TestDeployVerifyComparesTheGivenIndexDigest(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	testenv.RequireDocker(t)
	cli := deployTestClient(t)
	for _, name := range []string{"tack-app-1", "tack-audit-consumer-1"} {
		if _, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{}); err == nil {
			t.Fatalf("container %s exists on this daemon; the test refuses to replace it", name)
		}
	}
	registry, hostPort := startDeployRegistry(t, cli)
	tag := "m13-" + uuid.NewString()[:8]
	serverDigest := registry.pushMultiPlatformIndex(t, "tack-server", tag, "server "+tag)
	consumerDigest := registry.pushMultiPlatformIndex(t, "tack-audit-consumer", tag, "consumer "+tag)
	registryHost := "localhost:" + hostPort
	createFromIndex(t, cli, "tack-app-1", registryHost+"/tack-server:"+tag)
	createFromIndex(t, cli, "tack-audit-consumer-1", registryHost+"/tack-audit-consumer:"+tag)

	logDeployImageReads(t, cli, "tack-app-1")

	cfg := &config.Config{DeployRegistry: registryHost, DeployImageTag: tag}
	output, err := runOpsDeployVerify(t, cfg, ledgerDSN, serverDigest, consumerDigest)
	if err != nil {
		t.Fatalf("deploy verify with the pushed index digests: %v\n%s", err, output)
	}
	t.Logf("matching digests: %s", strings.TrimSpace(output))
	output, err = runOpsDeployVerify(t, cfg, ledgerDSN, consumerDigest, consumerDigest)
	if err == nil || !strings.Contains(err.Error(), "descriptor digest differs") || !strings.Contains(err.Error(), "tack-app-1") {
		t.Fatalf("deploy verify with the other index digest for tack-server = %v, want a refusal for tack-app-1\n%s", err, output)
	}
	t.Logf("mismatching digest refused: %v", err)
}

// runOpsDeployVerify runs the audited ops deploy verify command with the two
// digest flags and the real operator outbox.
func runOpsDeployVerify(t *testing.T, cfg *config.Config, ledgerDSN, serverDigest, consumerDigest string) (string, error) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	defer pool.Close()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	defer factory.CloseAuditOutbox()
	root := searchCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs([]string{
		"--execute", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com", "--operator-name", "Deploy Verify Test",
		"ops", "deploy", "verify", "--tack-server-digest", serverDigest, "--tack-audit-consumer-digest", consumerDigest,
	})
	runErr := root.Execute()
	return output.String(), runErr
}

func deployTestClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// startDeployRegistry starts a registry on the testenv network with its API
// port published on the daemon host loopback. It returns a registry client
// for the test process and the published host port the daemon pulls from.
func startDeployRegistry(t *testing.T, cli *client.Client) (localRegistry, string) {
	t.Helper()
	pullImage(t, cli, deployRegistryImage)
	name := "tack-testenv-registry-" + uuid.NewString()[:8]
	apiPort := network.MustParsePort("5000/tcp")
	_, err := cli.ContainerCreate(t.Context(), client.ContainerCreateOptions{
		Config: &container.Config{
			Image: deployRegistryImage, ExposedPorts: network.PortSet{apiPort: {}},
			Labels: map[string]string{deployTestenvLabel: "true"},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{apiPort: {{HostPort: ""}}}},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{deployTestenvNetwork: {}},
		},
		Name: name,
	})
	if err != nil {
		t.Fatalf("create the registry: %v", err)
	}
	t.Cleanup(func() { removeDeployContainer(t, cli, name) })
	if _, err := cli.ContainerStart(t.Context(), name, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start the registry: %v", err)
	}
	inspected, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.NetworkSettings == nil || len(inspected.Container.NetworkSettings.Ports[apiPort]) == 0 {
		t.Fatalf("read the registry port: %v", err)
	}
	hostPort := inspected.Container.NetworkSettings.Ports[apiPort][0].HostPort
	base := "http://" + name + ":5000"
	if _, err := net.DefaultResolver.LookupHost(t.Context(), name); err != nil {
		base = "http://localhost:" + hostPort
	}
	registry := localRegistry{base: base, http: &http.Client{Timeout: 30 * time.Second}}
	waitForRegistry(t, registry)
	return registry, hostPort
}

func waitForRegistry(t *testing.T, registry localRegistry) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, registry.base+"/v2/", nil)
		if err != nil {
			t.Fatalf("build the registry probe: %v", err)
		}
		if response, err := registry.http.Do(request); err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the %s did not answer /v2/ within a minute", registry.base)
}
