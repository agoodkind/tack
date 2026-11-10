//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"context"
	"io"
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

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureImageBeforeHelper(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "DiskImageKilled")
	matcher := &macFixtureDiskEvents{project: project, imageBeforeHelper: true, docker: docker}
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
	delayed := make(chan struct{})
	release := make(chan struct{})
	var delayedOnce, releaseOnce sync.Once
	proxy.ModifyResponse = func(response *http.Response) error {
		if strings.HasSuffix(response.Request.URL.Path, "/events") {
			reader, writer := io.Pipe()
			original := response.Body
			response.Body = reader
			go macDelayDiskEvent(
				response.Request.Context(),
				original,
				writer,
				delayed,
				release,
				&delayedOnce,
				matcher,
			)
		}
		if strings.HasSuffix(response.Request.URL.Path, "/wait") {
			select {
			case <-delayed:
			case <-response.Request.Context().Done():
				return response.Request.Context().Err()
			}
		}
		return nil
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet && matcher.matchesInventory(request.URL.Path) {
				releaseOnce.Do(func() { close(release) })
			}
			proxy.ServeHTTP(writer, request)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); transport.CloseIdleConnections() })
	t.Setenv("HOME", fixtureHome)
	result, runErr := testenv.RunMacTests(t.Context(), options)
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	matcher.Lock()
	name := matcher.dataName
	matcher.Unlock()
	t.Cleanup(func() {
		if name == "" {
			return
		}
		ctx := context.Background()
		volume, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if err != nil || volume.Volume.Labels["io.goodkind.tack.testenv"] != "true" ||
			volume.Volume.Labels["io.goodkind.tack.testenv.runner"] != summary.RunnerID {
			t.Errorf("refuse image volume recovery %s: %v", name, err)
			return
		}
		if _, err := docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
			t.Error(err)
		}
	})
	if runErr == nil || result.ExitCode != 1 || summary.Attribution || summary.Cleanup ||
		summary.FullCleanup {
		t.Errorf(
			"pre-helper volume reported complete cleanup: result=%+v error=%v summary=%+v",
			result,
			runErr,
			summary,
		)
	}
	select {
	case <-delayed:
	default:
		t.Error("the exact image volume creation event was not delayed")
	}
	select {
	case <-release:
	default:
		t.Error("the runner did not list disk volumes after the interruption")
	}
	proven, found := summary.Fixtures.Volumes[name]
	preserved := summary.Fixtures.Preserved["volume:"+name]
	if name == "" || !found || proven || !preserved.Incomplete ||
		len(summary.Fixtures.Containers) != 0 {
		t.Errorf("pre-helper image volume was not preserved as unproven: %+v", summary.Fixtures)
	}
	if _, err := docker.VolumeInspect(
		t.Context(),
		name,
		client.VolumeInspectOptions{},
	); err != nil {
		t.Errorf("pre-helper image volume disappeared: %v", err)
	}
}
