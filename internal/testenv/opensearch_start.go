package testenv

import (
	"context"
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
	// or above 85 percent.
	openSearchJavaOptions = "-Xms3g -Xmx3g -XX:G1PeriodicGCInterval=5000"
)

// provisionOpenSearch starts one engine and returns the fixture after
// authenticated health succeeds, with the names of every container it
// created, also on failure. The engine shares the network stack of an
// address holder container that only sleeps. The node certificate and the
// fixture endpoint both use the holder's address. A process on the Docker host
// and a process on the tack-testenv network both connect to that address. The
// address stays assigned while the engine is stopped.
func provisionOpenSearch(
	ctx context.Context,
	settings openSearchEngineSettings,
) (OpenSearchFixture, []string, error) {
	var created []string
	cli, err := dockerClient(ctx)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	defer func() { _ = cli.Close() }()
	holder, err := startOpenSearchAddress(ctx, cli)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	created = append(created, holder.name)
	suffix, err := randomHex(ctx, 4)
	if err != nil {
		wrapped := fmt.Errorf("generate OpenSearch container suffix: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.fixture_name_failed",
			slog.String("err", wrapped.Error()),
		)

		return OpenSearchFixture{}, created, wrapped
	}

	name := "tack-testenv-opensearch-" + strconv.Itoa(os.Getpid()) + "-" + suffix
	certificates, err := openSearchCertificatesForSettings(
		ctx,
		net.ParseIP(holder.address),
		settings.administratorDirectory,
	)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	securityFiles, username, pass, err := prepareOpenSearchSecurity(ctx, settings)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	owner, err := prepareCreate(ctx, cli)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	err = createOpenSearchContainer(ctx, cli, holder.name, settings, owner, name)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	own(name)
	created = append(created, name)

	err = installOpenSearchCertificates(ctx, cli, name, certificates)
	if err != nil {
		return OpenSearchFixture{}, created, err
	}

	if err := installOpenSearchSecurity(ctx, cli, name, securityFiles); err != nil {
		return OpenSearchFixture{}, created, err
	}

	if _, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		wrapped := fmt.Errorf("start OpenSearch fixture %s: %w", name, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.fixture_start_failed",
			slog.String("err", wrapped.Error()),
			slog.String("container", name),
		)

		return OpenSearchFixture{}, created, wrapped
	}

	fixture := OpenSearchFixture{
		Endpoint:  "https://" + net.JoinHostPort(holder.address, openSearchPort),
		CA:        string(certificates.ca),
		Username:  username,
		Password:  pass,
		Container: name,
	}
	healthContext, stopWaiting := context.WithCancelCause(ctx)
	defer stopWaiting(nil)
	watchOpenSearchExit(ctx, healthContext, cli, name, stopWaiting)
	if err := waitForOpenSearchHealth(healthContext, fixture); err != nil {
		return OpenSearchFixture{}, created, engineStartFailure(
			ctx,
			cli,
			name,
			errors.Join(err, context.Cause(healthContext)),
		)
	}

	logOpenSearchStarted(ctx, name, &settings)

	return fixture, created, nil
}

func createOpenSearchContainer(
	ctx context.Context,
	cli *client.Client,
	holderName string,
	settings openSearchEngineSettings,
	owner processOwner,
	name string,
) error {
	_, err := cli.ContainerCreate(
		ctx,
		client.ContainerCreateOptions{
			Config: &container.Config{
				Image: openSearchImage,
				Env: append(
					[]string{
						"discovery.type=single-node",
						"OPENSEARCH_JAVA_OPTS=" + openSearchJavaOptions,
						"DISABLE_INSTALL_DEMO_CONFIG=true",
					},
					settings.nodeSettings...,
				),
				Labels: ownerLabels(owner),
			},
			HostConfig:       openSearchHostConfig(holderName, settings),
			NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: nil},
			Name:             name,
		},
	)
	if err != nil {
		wrapped := fmt.Errorf("create OpenSearch fixture %s: %w", name, err)
		logger := telemetry.L(ctx)
		logger.ErrorContext(
			ctx,
			"search.fixture_create_failed",
			slog.String("err", wrapped.Error()),
			slog.String("container", name),
		)

		return wrapped
	}

	return nil
}

func installOpenSearchCertificates(
	ctx context.Context,
	cli *client.Client,
	name string,
	certificates openSearchCertificates,
) error {
	for path, contents := range map[string][]byte{
		"/usr/share/opensearch/config/opensearch.yml":     []byte(openSearchConfig),
		"/usr/share/opensearch/config/certs/ca.pem":       certificates.ca,
		"/usr/share/opensearch/config/certs/node.pem":     certificates.node,
		"/usr/share/opensearch/config/certs/node-key.pem": certificates.key,
	} {
		if err := writeContainerFileAs(
			ctx,
			cli,
			name,
			path,
			contents,
			openSearchOwner,
		); err != nil {
			return err
		}
	}

	return nil
}

func watchOpenSearchExit(
	ctx context.Context,
	healthContext context.Context,
	cli *client.Client,
	name string,
	stopWaiting context.CancelCauseFunc,
) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(
					ctx,
					"testenv.engine.wait_panicked",
					slog.String("err", fmt.Sprint(recovered)),
				)
			}
		}()
		cancelOnExit(healthContext, cli, name, stopWaiting)
	}()
}

func logOpenSearchStarted(ctx context.Context, name string, settings *openSearchEngineSettings) {
	telemetry.L(ctx).InfoContext(
		ctx,
		"search.fixture_started",
		slog.String("container", name),
		slog.Int64("memory_bytes", settings.memoryBytes),
		slog.String("data_volume", settings.dataVolume),
	)
}

func startOpenSearchAddress(ctx context.Context, cli *client.Client) (engine, error) {
	return startEngine(
		ctx,
		cli,
		engineSpec{
			kind:       "opensearch-address",
			image:      openSearchImage,
			platform:   nil,
			entrypoint: []string{"sleep"},
			cmd:        []string{openSearchHolderSeconds},
			env:        nil,
		},
	)
}
