package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// meilisearchService is the stack file service whose image the search
	// engine runs.
	meilisearchService = "meilisearch"
	// meilisearchPort is the engine's HTTP port.
	meilisearchPort = "7700"
	// meilisearchMasterKey is the master key every test engine starts with. The
	// engine holds no data beyond one test binary's run on a private network,
	// so a fixed key costs nothing and keeps the DSN a plain URL.
	meilisearchMasterKey = "tack-testenv-meilisearch-key"
	// meilisearchProbeTimeout bounds one readiness probe.
	meilisearchProbeTimeout = 5 * time.Second
	// meilisearchAvailable is the status the health endpoint reports once the
	// engine serves requests.
	meilisearchAvailable = `"available"`
)

var meilisearchState provisioned

// Meilisearch returns the URL of this process's search engine and the master
// key it accepts. The engine starts empty: a test indexes what it searches.
func Meilisearch(t T) (string, string) {
	t.Helper()
	skipWhenShort(t)
	return meilisearchState.get(t, provisionMeilisearch), meilisearchMasterKey
}

// provisionMeilisearch starts this process's search engine and returns its
// URL once the health endpoint reports it available.
func provisionMeilisearch(ctx context.Context) (string, error) {
	image, err := serviceImage(ctx, meilisearchService)
	if err != nil {
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	started, err := startEngine(ctx, cli, engineSpec{
		kind:     "meilisearch",
		image:    image,
		platform: nil,
		cmd:      nil,
		env: []string{
			"MEILI_MASTER_KEY=" + meilisearchMasterKey,
			"MEILI_ENV=development",
			"MEILI_MAX_INDEXING_MEMORY=1073741824",
			"MEILI_HTTP_ADDR=[::]:" + meilisearchPort,
		},
	})
	if err != nil {
		return "", err
	}
	url := "http://" + net.JoinHostPort(started.address, meilisearchPort)
	if err := waitForMeilisearch(ctx, url); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.meilisearch.ready", slog.String("container", started.name))
	return url, nil
}

// waitForMeilisearch polls the health endpoint until it reports the engine
// available, or fails when ctx ends with the last probe's error.
func waitForMeilisearch(ctx context.Context, url string) error {
	for {
		lastErr := probeMeilisearch(ctx, url)
		if lastErr == nil {
			return nil
		}
		if !sleepOrDone(ctx) {
			slog.ErrorContext(ctx, "testenv.meilisearch.not_ready", slog.String("err", lastErr.Error()))
			return fmt.Errorf("the test search engine did not answer before the deadline: %w", lastErr)
		}
	}
}

// probeMeilisearch makes one bounded health request. A failed probe is
// expected while the engine starts, so it logs at debug and returns the
// reason for the final deadline error.
func probeMeilisearch(ctx context.Context, url string) error {
	probeCtx, cancel := context.WithTimeout(ctx, meilisearchProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url+"/health", nil)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.meilisearch.probe_request_failed", slog.String("err", err.Error()))
		return fmt.Errorf("build the health request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		slog.DebugContext(ctx, "testenv.meilisearch.probe", slog.String("err", err.Error()))
		return errors.New("connect: " + err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		slog.DebugContext(ctx, "testenv.meilisearch.probe", slog.String("err", err.Error()))
		return errors.New("read health: " + err.Error())
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), meilisearchAvailable) {
		slog.DebugContext(ctx, "testenv.meilisearch.probe", slog.String("body", string(body)))
		return fmt.Errorf("health %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
