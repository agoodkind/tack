//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureDiskDelayedEvent(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "DiskKilled")
	matcher := &macFixtureDiskEvents{project: project}
	originalHome := os.Getenv("HOME")
	fixtureHome, err := os.MkdirTemp("/tmp", "tack-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixtureHome) })
	inspectionSocket := filepath.Join(fixtureHome, ".docker/run/docker.sock")
	rawSocket := filepath.Join(fixtureHome, "Library/Containers/com.docker.docker/Data/docker.raw.sock")
	for _, directory := range []string{filepath.Dir(inspectionSocket), filepath.Dir(rawSocket)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(originalHome, "Library/Containers/com.docker.docker/Data/docker.raw.sock"), rawSocket); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", inspectionSocket)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(originalHome, ".docker/run/docker.sock"))
	}}
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "docker"})
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
			go macDelayDiskEvent(response.Request.Context(), original, writer, delayed, release, &delayedOnce, matcher)
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
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && matcher.matchesInspection(request.URL.Path) {
			releaseOnce.Do(func() { close(release) })
		}
		proxy.ServeHTTP(writer, request)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); transport.CloseIdleConnections() })
	t.Setenv("HOME", fixtureHome)
	result, runErr := testenv.RunMacTests(t.Context(), options)
	if runErr != nil || result.ExitCode != 1 {
		t.Errorf("delayed event runner: result=%+v error=%v", result, runErr)
	}
	select {
	case <-delayed:
	default:
		t.Error("the real data-volume event was not delayed")
	}
	select {
	case <-release:
	default:
		t.Error("the runner did not inspect the data volume before cleanup")
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	if !summary.Attribution || !summary.Cleanup || !summary.FullCleanup || len(summary.Fixtures.Containers) != 1 || len(summary.Fixtures.NamedVolumes) != 2 {
		t.Errorf("delayed event prevented complete owned cleanup: %+v", summary)
	}
	for id := range summary.Fixtures.Containers {
		if _, err := docker.ContainerInspect(t.Context(), id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("disk helper remains %s: %v", id, err)
		}
	}
	for name := range summary.Fixtures.NamedVolumes {
		if _, err := docker.VolumeInspect(t.Context(), name, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Errorf("disk volume remains %s: %v", name, err)
		}
	}
}

func macDelayDiskEvent(ctx context.Context, source io.ReadCloser, destination *io.PipeWriter, delayed, release chan struct{}, once *sync.Once, matcher *macFixtureDiskEvents) {
	defer source.Close()
	defer destination.Close()
	scanner := bufio.NewScanner(source)
	for scanner.Scan() {
		line := scanner.Bytes()
		if matcher.matchesCreate(line) {
			once.Do(func() { close(delayed) })
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
		}
		if _, err := destination.Write(append(line, '\n')); err != nil {
			return
		}
	}
}

type macFixtureDiskEvents struct {
	sync.Mutex
	project           string
	runnerID          string
	dataName          string
	imageBeforeHelper bool
	docker            *client.Client
}

func (matcher *macFixtureDiskEvents) matchesCreate(line []byte) bool {
	var event struct {
		Type   string `json:"Type"`
		Action string `json:"Action"`
		Actor  struct {
			ID         string            `json:"ID"`
			Attributes map[string]string `json:"Attributes"`
		} `json:"Actor"`
	}
	if json.Unmarshal(line, &event) != nil || event.Action != "create" {
		return false
	}
	matcher.Lock()
	defer matcher.Unlock()
	if event.Type == "container" && event.Actor.Attributes["com.docker.compose.project"] == matcher.project && event.Actor.Attributes["io.goodkind.tack.mac-runner.native"] == "true" {
		matcher.runnerID = event.Actor.ID
	}
	if event.Type == "container" && matcher.runnerID != "" && event.Actor.Attributes["io.goodkind.tack.testenv.runner"] == matcher.runnerID && strings.HasPrefix(event.Actor.Attributes["name"], "tack-testenv-opensearch-disk-") {
		matcher.dataName = event.Actor.Attributes["name"] + "-data"
	}
	if matcher.imageBeforeHelper && event.Type == "volume" && matcher.runnerID != "" && strings.HasPrefix(event.Actor.ID, "tack-testenv-opensearch-disk-") && strings.HasSuffix(event.Actor.ID, "-image") {
		inspection, err := matcher.docker.VolumeInspect(context.Background(), event.Actor.ID, client.VolumeInspectOptions{})
		if err == nil && inspection.Volume.Labels["io.goodkind.tack.testenv.runner"] == matcher.runnerID {
			matcher.dataName = event.Actor.ID
		}
	}
	return event.Type == "volume" && matcher.dataName != "" && event.Actor.ID == matcher.dataName
}

func (matcher *macFixtureDiskEvents) matchesInspection(path string) bool {
	matcher.Lock()
	defer matcher.Unlock()
	return matcher.dataName != "" && strings.HasSuffix(path, "/volumes/"+matcher.dataName)
}

func (matcher *macFixtureDiskEvents) matchesInventory(path string) bool {
	matcher.Lock()
	defer matcher.Unlock()
	return matcher.dataName != "" && strings.HasSuffix(path, "/volumes")
}
