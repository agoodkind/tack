//go:build darwin && integration && macrunnerfixture

package testenv_test

import (
	"bufio"
	"bytes"
	"context"
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

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerDockerEventLimitReportsIncomplete(t *testing.T) {
	options, docker, project := macOwnershipFixture(t, "DiskKilled")
	matcher := &macFixtureDiskEvents{project: project}
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
	limited := make(chan struct{})
	proxy.ModifyResponse = func(response *http.Response) error {
		if strings.HasSuffix(response.Request.URL.Path, "/wait") {
			select {
			case <-limited:
			case <-response.Request.Context().Done():
				return response.Request.Context().Err()
			}
		}
		if !strings.HasSuffix(response.Request.URL.Path, "/events") {
			return nil
		}
		reader, writer := io.Pipe()
		original := response.Body
		response.Body = reader
		go func() {
			defer original.Close()
			defer writer.Close()
			scanner := bufio.NewScanner(original)
			for scanner.Scan() {
				line := scanner.Bytes()
				if matcher.matchesCreate(line) {
					padding := bytes.Repeat([]byte(" "), 32<<10)
					for index := 0; index < 1025; index++ {
						if _, err := writer.Write(padding); err != nil {
							break
						}
					}
					close(limited)
					return
				}
				if _, err := writer.Write(append(line, '\n')); err != nil {
					return
				}
			}
		}()
		return nil
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); transport.CloseIdleConnections() })
	t.Setenv("HOME", fixtureHome)
	result, runErr := testenv.RunMacTests(t.Context(), options)
	if runErr == nil || result.ExitCode != 1 ||
		!strings.Contains(runErr.Error(), "docker event stream exceeds 32 MiB") {
		t.Errorf("event limit did not fail the runner: result=%+v error=%v", result, runErr)
	}
	summary := macOwnershipEvidence(t, options.EvidenceDirectory)
	macRecoverInspectionFixture(t, docker, summary)
	if summary.Attribution || summary.Cleanup || summary.FullCleanup {
		t.Errorf("event limit reported complete cleanup: %+v", summary)
	}
	if len(summary.Fixtures.Containers) != 1 {
		t.Fatalf("event limit did not retain the disk helper: %+v", summary.Fixtures.Containers)
	}
	for id, fixture := range summary.Fixtures.Containers {
		if _, err := docker.ContainerInspect(
			t.Context(),
			id,
			client.ContainerInspectOptions{},
		); err != nil {
			t.Errorf("event limit removed the disk helper: %v", err)
		}
		if _, err := docker.VolumeInspect(
			t.Context(),
			fixture.Name+"-data",
			client.VolumeInspectOptions{},
		); err != nil {
			t.Errorf("event limit removed the data volume: %v", err)
		}
	}
	macRuntimeEvidence(t, options, docker)
}
