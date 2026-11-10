package testenv

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/moby/moby/client"
)

const (
	// containerTailLineBytes bounds one log line the tail reader accepts.
	containerTailLineBytes = 1 << 20
	// containerTailLinks bounds the symbolic links the tail reader follows.
	// A glog .INFO file is one link to the current log file.
	containerTailLinks = 2
)

// containerFileTail returns the final lines of the regular file at filePath
// in a container, at most the given count. The copy API reads a stopped container as well
// as a running one. The reader follows a symbolic link to its target.
func containerFileTail(ctx context.Context, cli *client.Client, containerName, filePath string, lines int) (string, error) {
	source := filePath
	for range containerTailLinks + 1 {
		tail, target, err := copiedFileTail(ctx, cli, containerName, source, lines)
		if err != nil || target == "" {
			return tail, err
		}
		if !path.IsAbs(target) {
			target = path.Join(path.Dir(source), target)
		}
		source = target
	}
	return "", fmt.Errorf("%s in %s: more than %d symbolic links", filePath, containerName, containerTailLinks)
}

// copiedFileTail copies source out of the container. It returns the final
// lines of a regular file, at most the given count, or the link target of a
// symbolic link.
func copiedFileTail(ctx context.Context, cli *client.Client, containerName, source string, lines int) (string, string, error) {
	copied, err := cli.CopyFromContainer(ctx, containerName, client.CopyFromContainerOptions{SourcePath: source})
	if err != nil {
		slog.DebugContext(ctx, "testenv.copy.unavailable", slog.String("err", err.Error()))
		return "", "", errors.New("copy " + source + " from " + containerName + ": " + err.Error())
	}
	defer func() { _ = copied.Content.Close() }()
	archive := tar.NewReader(copied.Content)
	header, err := archive.Next()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.copy.read_failed", slog.String("err", err.Error()))
		return "", "", fmt.Errorf("read the archive of %s: %w", source, err)
	}
	if header.Typeflag == tar.TypeSymlink {
		return "", header.Linkname, nil
	}
	if header.Typeflag != tar.TypeReg {
		return "", "", fmt.Errorf("%s in %s is not a regular file", source, containerName)
	}
	scanner := bufio.NewScanner(archive)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), containerTailLineBytes)
	tail := make([]string, 0, lines)
	for scanner.Scan() {
		if len(tail) == lines {
			tail = tail[1:]
		}
		tail = append(tail, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.ErrorContext(ctx, "testenv.copy.read_failed", slog.String("err", err.Error()))
		return strings.Join(tail, "\n"), "", fmt.Errorf("read %s: %w", source, err)
	}
	return strings.Join(tail, "\n"), "", nil
}
