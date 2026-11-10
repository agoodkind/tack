//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerFixtureDiskInspectionFailure(t *testing.T) {
	options, docker, _ := macOwnershipFixture(t, "DiskKilled")
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
	var failedInspections atomic.Int32
	server := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet &&
				strings.Contains(request.URL.Path, "/volumes/tack-testenv-opensearch-disk-") &&
				strings.HasSuffix(request.URL.Path, "-data") {
				failedInspections.Add(1)
				http.Error(
					writer,
					"injected volume inspection failure",
					http.StatusServiceUnavailable,
				)
				return
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
	macRecoverInspectionFixture(t, docker, summary)
	if failedInspections.Load() == 0 || runErr == nil || result.ExitCode != 1 ||
		!strings.Contains(runErr.Error(), "injected volume inspection failure") {
		t.Errorf(
			"inspection failure result=%+v error=%v requests=%d",
			result,
			runErr,
			failedInspections.Load(),
		)
	}
	if summary.Attribution || summary.Cleanup || summary.FullCleanup {
		t.Errorf("inspection failure reported complete cleanup: %+v", summary)
	}
	if len(summary.Fixtures.Containers) != 1 {
		t.Fatalf("expected one retained disk helper, got %d", len(summary.Fixtures.Containers))
	}
	for id, fixture := range summary.Fixtures.Containers {
		name := fixture.Name + "-data"
		proven, candidate := summary.Fixtures.Volumes[name]
		preserved := summary.Fixtures.Preserved["volume:"+name]
		if !candidate || proven || !preserved.Incomplete || fixture.Absent {
			t.Errorf(
				"failed inspection did not preserve the data volume and helper: %+v",
				summary.Fixtures,
			)
		}
		if _, err := docker.ContainerInspect(
			t.Context(),
			id,
			client.ContainerInspectOptions{},
		); err != nil {
			t.Errorf("disk helper is absent: %v", err)
		}
		if _, err := docker.VolumeInspect(
			t.Context(),
			name,
			client.VolumeInspectOptions{},
		); err != nil {
			t.Errorf("data volume is absent: %v", err)
		}
	}
}

func macRecoverInspectionFixture(t *testing.T, docker *client.Client, summary macOwnershipSummary) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for id, fixture := range summary.Fixtures.Containers {
			helper, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if err != nil || helper.Container.Config == nil ||
				!maps.Equal(helper.Container.Config.Labels, fixture.Labels) ||
				fixture.Labels["io.goodkind.tack.testenv.runner"] != summary.RunnerID ||
				helper.Container.Name != "/"+fixture.Name {
				t.Errorf("refuse disk helper recovery %s: %v", id, err)
				continue
			}
			dataName, imageName := fixture.Name+"-data", fixture.Name+"-image"
			data, err := docker.VolumeInspect(ctx, dataName, client.VolumeInspectOptions{})
			if err != nil || !macFixtureSameOwner(data.Volume.Labels, fixture.Labels) ||
				data.Volume.Driver != "local" ||
				data.Volume.Options["type"] != "ext4" ||
				!strings.HasPrefix(data.Volume.Options["device"], "/dev/loop") {
				t.Errorf("refuse disk volume recovery %s: %v", dataName, err)
				continue
			}
			image, err := docker.VolumeInspect(ctx, imageName, client.VolumeInspectOptions{})
			if err != nil || !macFixtureSameOwner(image.Volume.Labels, fixture.Labels) {
				t.Errorf("refuse image volume recovery %s: %v", imageName, err)
				continue
			}
			mounted := false
			for _, point := range helper.Container.Mounts {
				if point.Name == imageName && point.Destination == "/disk" {
					mounted = true
				}
			}
			output, err := macFixtureExec(
				ctx,
				docker,
				id,
				[]string{"losetup", "-j", "/disk/data.img"},
			)
			if !mounted || err != nil ||
				!strings.HasPrefix(output, data.Volume.Options["device"]+":") {
				t.Errorf(
					"refuse disk recovery without matching mount and loop device: %s: %v",
					output,
					err,
				)
				continue
			}
			if _, err := docker.VolumeRemove(
				ctx,
				dataName,
				client.VolumeRemoveOptions{},
			); err != nil {
				t.Error(err)
				continue
			}
			if _, err := macFixtureExec(
				ctx,
				docker,
				id,
				[]string{"losetup", "-d", data.Volume.Options["device"]},
			); err != nil {
				t.Error(err)
				continue
			}
			if _, err := docker.ContainerRemove(
				ctx,
				id,
				client.ContainerRemoveOptions{Force: true},
			); err != nil {
				t.Error(err)
				continue
			}
			if _, err := docker.VolumeRemove(
				ctx,
				imageName,
				client.VolumeRemoveOptions{},
			); err != nil {
				t.Error(err)
			}
		}
	})
}

func macFixtureSameOwner(left, right map[string]string) bool {
	for _, key := range []string{
		"io.goodkind.tack.testenv",
		"io.goodkind.tack.testenv.runner",
		"io.goodkind.tack.testenv.host",
		"io.goodkind.tack.testenv.process",
		"io.goodkind.tack.testenv.run",
	} {
		if left[key] == "" || left[key] != right[key] {
			return false
		}
	}
	return true
}

func macFixtureExec(
	ctx context.Context,
	docker *client.Client,
	id string,
	command []string,
) (string, error) {
	created, err := docker.ExecCreate(
		ctx,
		id,
		client.ExecCreateOptions{Cmd: command, AttachStdout: true, AttachStderr: true},
	)
	if err != nil {
		return "", err
	}
	attached, err := docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, attached.Reader); err != nil && err != io.EOF {
		return "", err
	}
	inspection, err := docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", err
	}
	if inspection.ExitCode != 0 {
		return "", fmt.Errorf("fixture exec exited %d: %s", inspection.ExitCode, output.String())
	}
	return output.String(), nil
}
