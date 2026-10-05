package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// openSearchAdminUser is the internal user the fixture creates and returns.
	openSearchAdminUser = "admin"
	// openSearchHolderSeconds is how long the address holder sleeps, which is
	// longer than any test binary runs.
	openSearchHolderSeconds = "2147483647"
	// openSearchPort is the engine's HTTPS port.
	openSearchPort = "9200"
	// The ML Commons memory breaker rejects predictions when used heap is at
	// or above 85 percent. G1PeriodicGCInterval=5000 enables periodic garbage
	// collection.
	openSearchJavaOptions = "-Xms3g -Xmx3g -XX:G1PeriodicGCInterval=5000"
)

// provisionOpenSearch starts one engine and returns the fixture after
// authenticated health succeeds, with the names of every container it
// created, also on failure. The engine shares the network stack of an
// address holder container that only sleeps. The node certificate and the
// fixture endpoint both use the holder's address. A process on the Docker host
// and a process on the tack-testenv network both connect to that address. The
// address stays assigned while the engine is stopped.
func provisionOpenSearch(ctx context.Context, settings openSearchEngineSettings) (OpenSearchFixture, []string, error) {
	var created []string
	cli, err := dockerClient(ctx)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}
	defer func() { _ = cli.Close() }()
	holder, err := startEngine(ctx, cli, engineSpec{
		kind: "opensearch-address", image: openSearchImage, platform: nil,
		entrypoint: []string{"sleep"}, cmd: []string{openSearchHolderSeconds}, env: nil,
	})
	if err != nil {
		return OpenSearchFixture{}, created, err
	}
	created = append(created, holder.name)
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		wrapped := fmt.Errorf("generate OpenSearch container suffix: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_name_failed", slog.String("err", wrapped.Error()))
		return OpenSearchFixture{}, created, wrapped
	}
	name := "tack-testenv-opensearch-" + strconv.Itoa(os.Getpid()) + "-" + suffix
	certificates, err := makeOpenSearchCertificates(ctx, net.ParseIP(holder.address))
	if err != nil {
		return OpenSearchFixture{}, created, err
	}
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		wrapped := fmt.Errorf("generate OpenSearch admin password: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_password_failed", slog.String("err", wrapped.Error()))
		return OpenSearchFixture{}, created, wrapped
	}
	pass := hex.EncodeToString(passwordBytes)
	internalUsers, err := openSearchInternalUsers(ctx, openSearchAdminUser, pass)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}
	_, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: openSearchImage,
			Env: append([]string{
				"discovery.type=single-node",
				"OPENSEARCH_JAVA_OPTS=" + openSearchJavaOptions,
				"DISABLE_INSTALL_DEMO_CONFIG=true",
			}, settings.nodeSettings...),
			Labels: map[string]string{managedLabel: "true"},
		},
		HostConfig:       openSearchHostConfig(holder.name, settings),
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: nil},
		Name:             name,
	})
	if err != nil {
		wrapped := fmt.Errorf("create OpenSearch fixture %s: %w", name, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_create_failed", slog.String("err", wrapped.Error()), slog.String("container", name))
		return OpenSearchFixture{}, created, wrapped
	}
	own(name)
	created = append(created, name)
	for path, contents := range map[string][]byte{
		"/usr/share/opensearch/config/opensearch.yml":                         []byte(openSearchConfig),
		"/usr/share/opensearch/config/opensearch-security/internal_users.yml": internalUsers,
		"/usr/share/opensearch/config/certs/ca.pem":                           certificates.ca,
		"/usr/share/opensearch/config/certs/node.pem":                         certificates.node,
		"/usr/share/opensearch/config/certs/node-key.pem":                     certificates.key,
	} {
		if err := writeContainerFileAs(ctx, cli, name, path, contents, openSearchOwner); err != nil {
			return OpenSearchFixture{}, created, err
		}
	}
	if _, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		wrapped := fmt.Errorf("start OpenSearch fixture %s: %w", name, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_start_failed", slog.String("err", wrapped.Error()), slog.String("container", name))
		return OpenSearchFixture{}, created, wrapped
	}
	fixture := OpenSearchFixture{
		Endpoint: "https://" + net.JoinHostPort(holder.address, openSearchPort), CA: string(certificates.ca),
		Username: openSearchAdminUser, Password: pass, Container: name,
	}
	healthContext, stopWaiting := context.WithCancelCause(ctx)
	defer stopWaiting(nil)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "testenv.engine.wait_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		cancelOnExit(healthContext, cli, name, stopWaiting)
	}()
	if err := waitForOpenSearchHealth(healthContext, fixture); err != nil {
		return OpenSearchFixture{}, created, engineStartFailure(ctx, cli, name, errors.Join(err, context.Cause(healthContext)))
	}
	telemetry.L(ctx).InfoContext(ctx, "search.fixture_started", slog.String("container", name),
		slog.Int64("memory_bytes", settings.memoryBytes), slog.String("data_volume", settings.dataVolume))
	return fixture, created, nil
}
