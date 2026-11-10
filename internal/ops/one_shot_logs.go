package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func readOneShotLogs(
	ctx context.Context,
	cli *client.Client,
	log *slog.Logger,
	image string,
	containerID string,
	exitCode int,
) (execResult, error) {
	logsRdr, err := cli.ContainerLogs(
		ctx,
		containerID,
		client.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
		},
	)
	if err != nil {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.logs_failed",
			slog.String("image", image),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("logs one-shot container: %w", err)
	}
	defer logsRdr.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, logsRdr)
	if err != nil && !errors.Is(err, io.EOF) {
		log.ErrorContext(
			ctx,
			"ops.docker.run_once.stream_failed",
			slog.String("image", image),
			slog.String("err", err.Error()),
		)

		return execResult{}, fmt.Errorf("read one-shot logs: %w", err)
	}

	return execResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}
