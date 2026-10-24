package ops

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// runPrivateOneShot captures output through an attachment before startup.
// The caller disables Docker logging and removes the container before returning.
func runPrivateOneShot(ctx context.Context, cli *client.Client, identifier string, files map[string][]byte) (execResult, error) {
	if err := copyPrivateOneShotFiles(ctx, cli, identifier, files); err != nil {
		return execResult{}, err
	}
	attached, err := cli.ContainerAttach(ctx, identifier, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return execResult{}, privateOneShotError(ctx, "attach private container output", err)
	}
	defer attached.Close()
	stopClosing := context.AfterFunc(ctx, attached.Close)
	defer stopClosing()
	if _, err := cli.ContainerStart(ctx, identifier, client.ContainerStartOptions{}); err != nil {
		return execResult{}, privateOneShotError(ctx, "start private container", err)
	}
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil && !errors.Is(err, io.EOF) {
		return execResult{}, privateOneShotError(ctx, "capture private container output", err)
	}
	waited := cli.ContainerWait(ctx, identifier, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var exitCode int
	select {
	case result := <-waited.Result:
		exitCode = int(result.StatusCode)
	case err := <-waited.Error:
		return execResult{}, privateOneShotError(ctx, "wait for private container", err)
	case <-ctx.Done():
		return execResult{}, privateOneShotError(ctx, "wait for private container", ctx.Err())
	}
	return execResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
}
