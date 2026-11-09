//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerRejectsNamedTemporaryVolume(t *testing.T) {
	options, docker, project := macDockerFixture(t)
	volumeName := project + "-named-temporary"
	var runnerID string
	t.Cleanup(func() {
		ctx := context.Background()
		if runnerID != "" {
			current, err := docker.ContainerInspect(ctx, runnerID, client.ContainerInspectOptions{})
			if err == nil && current.Container.Config != nil &&
				current.Container.Config.Labels["com.docker.compose.project"] == project &&
				current.Container.Config.Labels["io.goodkind.tack.mac-runner.root"] == options.Root {
				if _, err := docker.ContainerRemove(
					ctx,
					runnerID,
					client.ContainerRemoveOptions{Force: true},
				); err != nil {
					t.Error(err)
					return
				}
			} else if !errdefs.IsNotFound(err) {
				t.Errorf("refuse changed runner recovery: %v", err)
				return
			}
		}
		volume, err := docker.VolumeInspect(ctx, volumeName, client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			return
		}
		if err != nil || volume.Volume.Labels["tack.mac-runner-fixture"] != project {
			t.Errorf("refuse changed named volume recovery: %v", err)
			return
		}
		if _, err := docker.VolumeRemove(
			ctx,
			volumeName,
			client.VolumeRemoveOptions{},
		); err != nil {
			t.Error(err)
		}
	})
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
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPost &&
				strings.HasSuffix(request.URL.Path, "/containers/create") {
				if _, err := docker.VolumeCreate(
					request.Context(),
					client.VolumeCreateOptions{
						Name:   volumeName,
						Labels: map[string]string{"tack.mac-runner-fixture": project},
					},
				); err != nil {
					http.Error(writer, err.Error(), http.StatusInternalServerError)
					return
				}
				if err := macReplaceTemporaryMount(request, volumeName); err != nil {
					http.Error(writer, err.Error(), http.StatusBadRequest)
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
	result, runErr := testenv.RunMacTests(t.Context(), options)
	contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary macRuntimeSummary
	if err := json.Unmarshal(contents, &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.CreatedIDs) == 1 {
		runnerID = summary.CreatedIDs[0]
	}
	if strings.Contains(
		macRead(t, options.EvidenceDirectory, "baseline-volumes.stdout"),
		volumeName,
	) {
		t.Fatal("named volume existed in the baseline")
	}
	if runErr == nil || result.ExitCode != 1 ||
		!strings.Contains(runErr.Error(), "anonymous volume proof") ||
		runnerID == "" {
		t.Errorf("named temporary mount was not refused: result=%+v error=%v", result, runErr)
	}
	if _, err := docker.VolumeInspect(
		t.Context(),
		volumeName,
		client.VolumeInspectOptions{},
	); err != nil {
		t.Errorf("named volume was not preserved: %v", err)
	}
	if _, err := docker.ContainerInspect(
		t.Context(),
		runnerID,
		client.ContainerInspectOptions{},
	); err != nil {
		t.Errorf("runner with unproven mount was not preserved: %v", err)
	}
}

func macReplaceTemporaryMount(request *http.Request, name string) error {
	contents, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	var configuration struct {
		HostConfig container.HostConfig `json:"HostConfig"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(contents, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(contents, &configuration); err != nil {
		return err
	}
	found := false
	for index := range configuration.HostConfig.Mounts {
		point := &configuration.HostConfig.Mounts[index]
		if point.Target == "/tmp/tack-test" {
			point.Source = name
			found = true
		}
	}
	if !found {
		return io.ErrUnexpectedEOF
	}
	hostConfig, err := json.Marshal(configuration.HostConfig)
	if err != nil {
		return err
	}
	raw["HostConfig"] = hostConfig
	contents, err = json.Marshal(raw)
	if err != nil {
		return err
	}
	request.Body = io.NopCloser(bytes.NewReader(contents))
	request.ContentLength = int64(len(contents))
	return nil
}
