package ops

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const scratchMeiliImage = "getmeili/meilisearch:v1.12"

// restoreMeilisearch replays a Meilisearch volume tar into a fresh scratch
// container. Mirrors restore_meilisearch in
// scripts/backup-restore-test.sh:535-574. The shape check is /health
// returning 200; we do not verify per-index data integrity because the
// 2026-05-09 audit retro made that out of scope for the restore-test gate.
func restoreMeilisearch(ctx context.Context, r *restoreCtx, artifactPath string) error {
	label := filepath.Base(artifactPath)
	r.Log.InfoContext(ctx, "backup.restore_meili.start", slog.String("artifact", label))

	volumeName := "tack-rt-meili-data-" + r.RunID
	containerName := "tack-rt-meili-" + r.RunID

	_, err := r.Cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volumeName})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_meili.volume_create_failed",
			slog.String("name", volumeName),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("volume create %s: %w", volumeName, err)
	}
	r.trackVolume(volumeName)

	args := []string{
		"run", "--rm",
		"-v", volumeName + ":/dst",
		"-v", filepath.Dir(artifactPath) + ":/src:ro",
		"alpine", "sh", "-c",
		"cd /dst && tar -xzf /src/" + filepath.Base(artifactPath),
	}
	_, err = runDockerCmd(ctx, r.Log, args...)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_meili.populate_failed",
			slog.String("volume", volumeName),
			slog.Any("err", err),
		)
		return fmt.Errorf("populate scratch volume: %w", err)
	}

	cfg := &container.Config{
		Image: scratchMeiliImage,
		Env: []string{
			"MEILI_MASTER_KEY=rt-scratch",
			"MEILI_NO_ANALYTICS=true",
		},
	}
	hostCfg := &container.HostConfig{
		Binds: []string{volumeName + ":/meili_data"},
	}
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       containerName,
	})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_meili.create_failed",
			slog.String("name", containerName),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("create scratch meili: %w", err)
	}
	_, err = r.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_meili.start_failed",
			slog.String("name", containerName),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("start scratch meili: %w", err)
	}
	r.trackContainer(containerName)

	err = waitForExec(ctx, r.Cli, containerName, 60*time.Second,
		[]string{"wget", "-q", "-O-", "http://127.0.0.1:7700/health"})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_meili.health_timeout",
			slog.String("container", containerName),
			slog.Any("err", err),
		)
		return fmt.Errorf("scratch meili /health never returned: %w", err)
	}
	r.Log.InfoContext(ctx, "backup.restore_meili.ok",
		slog.String("artifact", label),
	)
	return nil
}
