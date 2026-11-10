// ledger_node_launcher_config.go moves the ledger node's saved launcher config
// in and out of its container. Both directions go through the Docker API as a
// tar stream, which works on a stopped container and on a running one alike,
// so the command never opens the data volume from the host.

package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// launcherConfigFile is a saved config as read from the container: its bytes
// and the tar header they came with, so a rewrite keeps the file's owner and
// mode and the launcher, which runs as that owner, can still write it.
type launcherConfigFile struct {
	header  tar.Header
	content []byte
}

// readLauncherConfig copies the saved config out of the named container.
// found is false when the container or the file does not exist, which is the
// state of a node that has never started; every other failure is an error.
func readLauncherConfig(ctx context.Context, cli *client.Client, containerName string) (launcherConfigFile, bool, error) {
	return readContainerFile(ctx, cli, containerName, ledgerLauncherConfigDir+"/"+ledgerLauncherConfigName)
}

// readContainerFile copies one file out of the named container. found is
// false when the container or the file does not exist; every other failure is
// an error.
func readContainerFile(ctx context.Context, cli *client.Client, containerName, path string) (launcherConfigFile, bool, error) {
	var none launcherConfigFile
	copied, err := cli.CopyFromContainer(ctx, containerName, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return none, false, nil
		}
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.read_failed", slog.String("err", err.Error()))
		return none, false, fmt.Errorf("read %s from %s: %w", path, containerName, err)
	}
	defer func() { _ = copied.Content.Close() }()
	file, err := firstTarFile(ctx, copied.Content)
	if err != nil {
		return none, false, fmt.Errorf("read %s from %s: %w", path, containerName, err)
	}
	return file, true, nil
}

// firstTarFile returns the first regular file in a tar stream.
func firstTarFile(ctx context.Context, stream io.Reader) (launcherConfigFile, error) {
	var none launcherConfigFile
	reader := tar.NewReader(stream)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return none, errors.New("the archive holds no regular file")
		}
		if err != nil {
			slog.ErrorContext(ctx, "ops.ledger.node_prepare.read_failed", slog.String("err", err.Error()))
			return none, fmt.Errorf("read archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			slog.ErrorContext(ctx, "ops.ledger.node_prepare.read_failed", slog.String("err", err.Error()))
			return none, fmt.Errorf("read archive entry %s: %w", header.Name, err)
		}
		return launcherConfigFile{header: *header, content: content}, nil
	}
}

// writeLauncherConfig copies content into the container as the saved config,
// under the owner and mode the file had when it was read.
func writeLauncherConfig(ctx context.Context, cli *client.Client, containerName string, original tar.Header, content []byte) error {
	archive, err := launcherConfigArchive(ctx, original, content)
	if err != nil {
		return fmt.Errorf("build archive for %s: %w", ledgerLauncherConfigName, err)
	}
	_, err = cli.CopyToContainer(ctx, containerName, client.CopyToContainerOptions{
		DestinationPath: ledgerLauncherConfigDir, Content: archive,
		AllowOverwriteDirWithFile: false, CopyUIDGID: false,
	})
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write %s into %s: %w", ledgerLauncherConfigName, containerName, err)
	}
	return nil
}

// launcherConfigArchive packs content as the one-file tar stream the copy
// takes, carrying the original owner, group, and mode.
func launcherConfigArchive(ctx context.Context, original tar.Header, content []byte) (*bytes.Buffer, error) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	header := tar.Header{
		Typeflag: tar.TypeReg,
		Name:     ledgerLauncherConfigName,
		Mode:     original.Mode,
		Uid:      original.Uid,
		Gid:      original.Gid,
		Uname:    original.Uname,
		Gname:    original.Gname,
		Size:     int64(len(content)),
		ModTime:  opsNow(),
	}
	err := writer.WriteHeader(&header)
	if err == nil {
		_, err = writer.Write(content)
	}
	if err == nil {
		err = writer.Close()
	}
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.write_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("pack %s: %w", ledgerLauncherConfigName, err)
	}
	return &archive, nil
}
