package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const privateOneShotDirectory = "/run/ops-private"

func oneShotHostConfig(options runOneShotOptions) *container.HostConfig {
	configuration := &container.HostConfig{Binds: options.Binds, ExtraHosts: options.ExtraHosts}
	if options.PrivateOutput || len(options.PrivateFiles) > 0 {
		configuration.LogConfig = container.LogConfig{Type: "none", Config: nil}
	}
	return configuration
}

// copyPrivateOneShotFiles supplies protected files without adding their bytes
// to Docker command or environment metadata.
func copyPrivateOneShotFiles(ctx context.Context, cli *client.Client, identifier string, files map[string][]byte) error {
	if len(files) == 0 {
		return nil
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	directory := strings.TrimPrefix(privateOneShotDirectory, "/")
	if err := writer.WriteHeader(&tar.Header{Name: directory + "/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		return privateOneShotError(ctx, "create private directory archive", err)
	}
	for name, body := range files {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("private container file requires a base filename")
		}
		if err := writer.WriteHeader(&tar.Header{Name: path.Join(directory, name), Mode: 0o600, Size: int64(len(body))}); err != nil {
			return privateOneShotError(ctx, "create private file archive", err)
		}
		if _, err := writer.Write(body); err != nil {
			return privateOneShotError(ctx, "write private file archive", err)
		}
	}
	if err := writer.Close(); err != nil {
		return privateOneShotError(ctx, "close private file archive", err)
	}
	_, err := cli.CopyToContainer(ctx, identifier, client.CopyToContainerOptions{DestinationPath: "/", Content: &archive})
	if err != nil {
		return privateOneShotError(ctx, "copy private container files", err)
	}
	return nil
}

func privateOneShotError(ctx context.Context, operation string, cause error) error {
	slog.ErrorContext(ctx, "ops.docker.private_command_failed", slog.String("operation", operation), slog.String("err", cause.Error()))
	return fmt.Errorf("%s: %w", operation, cause)
}
