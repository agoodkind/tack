package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// objectStoreImage is the S3-compatible store the backups land in. The
	// production object store is a configs-owned host, not a service of the
	// stack file, so its release is pinned here at the version the configs
	// repo installs.
	objectStoreImage = "chrislusf/seaweedfs:4.45"
	// objectStorePort is the store's S3 port.
	objectStorePort = "8333"
	// objectStoreProbeBucket is the bucket the readiness probe creates. A
	// store that accepts a bucket and an object in it has a writable volume,
	// which the S3 port answering alone does not prove.
	objectStoreProbeBucket = "tack-testenv-ready"
	// objectStoreProbeTimeout bounds one readiness probe.
	objectStoreProbeTimeout = 5 * time.Second
)

var objectStoreState provisioned

// ObjectStore returns the S3 endpoint URL of this process's object store. The
// store runs without identities, so it accepts any access key and secret, and
// it starts with no buckets but the readiness probe's.
func ObjectStore(t T) string {
	t.Helper()
	skipWhenShort(t)
	return objectStoreState.get(t, provisionObjectStore)
}

// provisionObjectStore starts this process's object store and returns its S3
// endpoint once it stores an object.
func provisionObjectStore(ctx context.Context) (string, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	started, err := startEngine(ctx, cli, engineSpec{
		kind:     "objectstore",
		image:    objectStoreImage,
		platform: nil,
		cmd:      []string{"server", "-s3"},
		env:      nil,
	})
	if err != nil {
		return "", err
	}
	endpoint := "http://" + net.JoinHostPort(started.address, objectStorePort)
	if err := waitForObjectStore(ctx, endpoint); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.objectstore.ready", slog.String("container", started.name))
	return endpoint, nil
}

// waitForObjectStore probes until the store keeps an object, or fails when ctx
// ends with the last probe's error.
func waitForObjectStore(ctx context.Context, endpoint string) error {
	for {
		lastErr := probeObjectStore(ctx, endpoint)
		if lastErr == nil {
			return nil
		}
		if !sleepOrDone(ctx) {
			slog.ErrorContext(ctx, "testenv.objectstore.not_ready", slog.String("err", lastErr.Error()))
			return fmt.Errorf("the test object store did not store an object before the deadline: %w", lastErr)
		}
	}
}

// probeObjectStore creates the probe bucket and writes one object into it. A
// failed probe is expected while the store starts, so it logs at debug.
func probeObjectStore(ctx context.Context, endpoint string) error {
	bucketURL := endpoint + "/" + objectStoreProbeBucket
	if err := putProbe(ctx, bucketURL); err != nil {
		return err
	}
	return putProbe(ctx, bucketURL+"/ready")
}

// putProbe makes one bounded, unsigned PUT and requires a 200 answer.
func putProbe(ctx context.Context, url string) error {
	probeCtx, cancel := context.WithTimeout(ctx, objectStoreProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodPut, url, http.NoBody)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.objectstore.probe_request_failed", slog.String("err", err.Error()))
		return fmt.Errorf("build the probe request for %s: %w", url, err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		slog.DebugContext(ctx, "testenv.objectstore.probe", slog.String("err", err.Error()))
		return errors.New("connect: " + err.Error())
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		slog.DebugContext(ctx, "testenv.objectstore.probe", slog.Int("status", response.StatusCode))
		return fmt.Errorf("PUT %s answered %d", url, response.StatusCode)
	}
	return nil
}
