//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

type macMissingFixture struct {
	sync.Mutex
	id     string
	name   string
	volume volume.Volume
}

func TestMacRunnerDockerFixtureMissingInspection(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "UnloggedCreate")
	fixture := &macMissingFixture{}
	t.Cleanup(func() { macRecoverMissingFixture(t, docker, fixture) })
	originalHome := os.Getenv("HOME")
	fixtureHome, err := os.MkdirTemp("/tmp", "tack-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixtureHome) })
	inspectionSocket := filepath.Join(fixtureHome, ".docker/run/docker.sock")
	rawSocket := filepath.Join(
		fixtureHome,
		"Library/Containers/com.docker.docker/Data/docker.raw.sock",
	)
	for _, directory := range []string{filepath.Dir(inspectionSocket), filepath.Dir(rawSocket)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(
		filepath.Join(originalHome, "Library/Containers/com.docker.docker/Data/docker.raw.sock"),
		rawSocket,
	); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", inspectionSocket)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(
				ctx,
				"unix",
				filepath.Join(originalHome, ".docker/run/docker.sock"),
			)
		},
	}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: "http",
		Host:   "docker",
	})
	proxy.Transport = transport
	proxy.FlushInterval = -1
	removed := make(chan struct{})
	var removedOnce sync.Once
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			id := macContainerInspectionID(request)
			if id != "" {
				matched, captureErr := macRemoveUnloggedFixture(
					request.Context(),
					docker,
					options.EvidenceDirectory,
					id,
					fixture,
				)
				if captureErr != nil {
					http.Error(writer, captureErr.Error(), http.StatusInternalServerError)
					return
				}
				if matched {
					removedOnce.Do(func() { close(removed) })
					http.Error(writer, "injected fixture disappearance", http.StatusNotFound)
					return
				}
			}
			proxy.ServeHTTP(writer, request)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); transport.CloseIdleConnections() })
	t.Setenv("HOME", fixtureHome)
	type outcome struct {
		result testenv.MacTestResult
		err    error
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	completed := make(chan outcome, 1)
	go func() {
		result, runErr := testenv.RunMacTests(ctx, options)
		completed <- outcome{result, runErr}
	}()
	finished := false
	t.Cleanup(func() {
		if !finished {
			cancel()
			select {
			case <-completed:
			case <-time.After(2 * time.Minute):
				t.Error("missing inspection runner did not finish")
			}
		}
	})
	select {
	case <-removed:
	case <-time.After(3 * time.Minute):
		t.Fatal("the exact unlogged fixture was not removed before inspection")
	}
	fixture.Lock()
	id, name, volumeName := fixture.id, fixture.name, fixture.volume.Name
	fixture.Unlock()
	deadline, stop := context.WithTimeout(t.Context(), time.Minute)
	defer stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !macHasCreateEvent(options.EvidenceDirectory, id) {
		select {
		case <-ticker.C:
		case <-deadline.Done():
			t.Fatal("the runner did not record the failed create inspection")
		}
	}
	cancel()
	result := <-completed
	finished = true
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	preserved := summary.Fixtures.Preserved["container:"+id]
	if result.err == nil || result.result.ExitCode != 130 || summary.Attribution ||
		summary.Cleanup ||
		summary.FullCleanup ||
		!preserved.Incomplete ||
		preserved.ID != id {
		t.Errorf(
			"missing inspection reported complete attribution: result=%+v error=%v summary=%+v",
			result.result,
			result.err,
			summary,
		)
	}
	if result.err == nil || !strings.Contains(result.err.Error(), "create inspection failed") ||
		strings.Contains(macRead(t, options.EvidenceDirectory, "runtime.log"), name) {
		t.Error("the missing inspection lacks its error or unexpectedly has a start log")
	}
	if summary.Fixtures.Volumes[volumeName] {
		t.Error("the runner attributed an unobserved anonymous volume")
	}
	if strings.Contains(
		macRead(t, options.EvidenceDirectory, "baseline-volumes.stdout"),
		volumeName,
	) {
		t.Error("the test volume existed before the runner baseline")
	}
	if _, err := docker.VolumeInspect(
		t.Context(),
		volumeName,
		client.VolumeInspectOptions{},
	); err != nil {
		t.Errorf("the unproven volume disappeared: %v", err)
	}
}
