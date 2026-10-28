//go:build integration

package ops_test

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
)

type retentionMetadataReport struct {
	URLCommands int
	Problems    []string
	Containers  []string
}

func observeRetentionMetadata(t *testing.T, cfg *config.Config) func() retentionMetadataReport {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal("create the synthetic retention metadata observer")
	}
	ctx, cancel := context.WithCancel(t.Context())
	events := cli.Events(ctx, client.EventsListOptions{Filters: client.Filters{}.Add("type", "container").Add("event", "start")})
	finished := make(chan retentionMetadataReport, 1)
	go func() {
		defer cli.Close()
		report := retentionMetadataReport{URLCommands: 0, Problems: nil, Containers: nil}
		defer func() { finished <- report }()
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-events.Err:
				if ctx.Err() == nil && err != nil {
					report.Problems = append(report.Problems, "the Docker event observer stopped before verification")
				}
				return
			case event := <-events.Messages:
				if event.Actor.Attributes["image"] != cfg.BackupFDBImage {
					continue
				}
				state, err := cli.ContainerInspect(ctx, event.Actor.ID, client.ContainerInspectOptions{})
				if err != nil {
					if !cerrdefs.IsNotFound(err) && ctx.Err() == nil {
						report.Problems = append(report.Problems, "inspect a synthetic retention container")
					}
					continue
				}
				owned := false
				for _, bind := range state.Container.HostConfig.Binds {
					owned = owned || strings.HasPrefix(bind, cfg.OpsFDBClusterDir+":")
				}
				if !owned {
					continue
				}
				report.Containers = append(report.Containers, event.Actor.ID)
				if problem := checkRetentionMetadata(ctx, cli, event.Actor.ID, cfg, state); problem != "" {
					report.Problems = append(report.Problems, problem)
				}
				for _, argument := range state.Container.Config.Cmd {
					if strings.HasPrefix(argument, "blobstore://") {
						report.URLCommands++
					}
				}
			}
		}
	}()
	var once sync.Once
	var report retentionMetadataReport
	stop := func() retentionMetadataReport {
		once.Do(func() {
			cancel()
			report = <-finished
			check, err := client.New(client.FromEnv)
			if err != nil {
				report.Problems = append(report.Problems, "create the synthetic credential cleanup observer")
				return
			}
			defer check.Close()
			for _, identifier := range report.Containers {
				if _, err := check.ContainerInspect(t.Context(), identifier, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
					report.Problems = append(report.Problems, "a retention container and its credential file survived command completion")
				}
			}
		})
		return report
	}
	t.Cleanup(func() { stop() })
	return stop
}

func checkRetentionMetadata(ctx context.Context, cli *client.Client, identifier string, cfg *config.Config, state client.ContainerInspectResult) string {
	for _, values := range [][]string{state.Container.Config.Cmd, state.Container.Config.Env} {
		for _, value := range values {
			for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
				if credential != "" && strings.Contains(value, credential) {
					return "synthetic S3 credentials appeared in Docker command or environment metadata"
				}
			}
		}
	}
	if state.Container.HostConfig.LogConfig.Type != "none" {
		return "retention did not disable persisted Docker logs"
	}
	credentialPath := ""
	for _, entry := range state.Container.Config.Env {
		if value, found := strings.CutPrefix(entry, "FDB_BLOB_CREDENTIALS="); found {
			credentialPath = value
		}
	}
	if credentialPath == "" {
		return "retention did not pass a native credential file reference"
	}
	for target, permission := range map[string]uint32{credentialPath: 0o600, path.Dir(credentialPath): 0o700} {
		copied, err := cli.CopyFromContainer(ctx, identifier, client.CopyFromContainerOptions{SourcePath: target})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				continue
			}
			gone, status := retentionContainerGone(ctx, cli, identifier)
			if gone {
				continue
			}
			return fmt.Sprintf("inspect protected synthetic credential file metadata: %v (container status %s)", err, status)
		}
		_ = copied.Content.Close()
		if uint32(copied.Stat.Mode.Perm()) != permission {
			return "retention did not apply protected credential file permissions"
		}
	}
	return ""
}

const (
	retentionRemovalPollInterval = 100 * time.Millisecond
	retentionRemovalTimeout      = 10 * time.Second
)

// retentionContainerGone waits up to retentionRemovalTimeout for Docker to report that a
// retention container does not exist. It returns false with the last observed status when
// the container still exists, or with the context error when ctx ends first.
func retentionContainerGone(ctx context.Context, cli *client.Client, identifier string) (bool, string) {
	waitCtx, cancel := context.WithTimeout(ctx, retentionRemovalTimeout)
	defer cancel()
	status := "unknown"
	for waitCtx.Err() == nil {
		state, err := cli.ContainerInspect(waitCtx, identifier, client.ContainerInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			return true, ""
		}
		if waitCtx.Err() == nil {
			status = retentionContainerStatus(state, err)
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(retentionRemovalPollInterval):
		}
	}
	if ctx.Err() != nil {
		return false, ctx.Err().Error()
	}
	return false, status
}

// retentionContainerStatus returns the inspect error text or the container state status.
func retentionContainerStatus(state client.ContainerInspectResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if state.Container.State == nil {
		return "unknown"
	}
	return string(state.Container.State.Status)
}
