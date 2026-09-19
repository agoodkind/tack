package testenv

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// maxCopiedFileSize bounds a file read out of a container; the only file read
// is a cluster file of one line.
const maxCopiedFileSize = 1 << 16

// execInContainer runs command inside a running container through the Docker
// SDK and returns its combined output and exit code.
func execInContainer(ctx context.Context, cli *client.Client, containerName string, command []string) (string, int, error) {
	created, err := cli.ExecCreate(ctx, containerName, client.ExecCreateOptions{
		Cmd: command, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.exec.create_failed", slog.String("err", err.Error()))
		return "", 0, fmt.Errorf("exec in %s: %w", containerName, err)
	}
	attached, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.exec.attach_failed", slog.String("err", err.Error()))
		return "", 0, fmt.Errorf("attach exec in %s: %w", containerName, err)
	}
	defer attached.Close()
	stopClosing := context.AfterFunc(ctx, attached.Close)
	defer stopClosing()
	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, attached.Reader); err != nil && !errors.Is(err, io.EOF) {
		slog.ErrorContext(ctx, "testenv.exec.stream_failed", slog.String("err", err.Error()))
		return "", 0, fmt.Errorf("read exec output in %s: %w", containerName, err)
	}
	inspected, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.exec.inspect_failed", slog.String("err", err.Error()))
		return "", 0, fmt.Errorf("inspect exec in %s: %w", containerName, err)
	}
	return output.String(), inspected.ExitCode, nil
}

// readContainerFile returns the contents of one regular file in a container.
func readContainerFile(ctx context.Context, cli *client.Client, containerName, path string) ([]byte, error) {
	copied, err := cli.CopyFromContainer(ctx, containerName, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		slog.DebugContext(ctx, "testenv.copy.unavailable", slog.String("err", err.Error()))
		return nil, errors.New("copy " + path + " from " + containerName + ": " + err.Error())
	}
	defer func() { _ = copied.Content.Close() }()
	archive := tar.NewReader(copied.Content)
	header, err := archive.Next()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.copy.read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the archive of %s: %w", path, err)
	}
	if header.Typeflag != tar.TypeReg {
		return nil, fmt.Errorf("%s in %s is not a regular file", path, containerName)
	}
	contents, err := io.ReadAll(io.LimitReader(archive, maxCopiedFileSize))
	if err != nil {
		slog.ErrorContext(ctx, "testenv.copy.read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return contents, nil
}
