package testenv

import (
	"context"
	"errors"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) captureFinalLogs(ctx context.Context) error {
	observation, cancel := context.WithTimeout(ctx, macRunnerObservation)
	defer cancel()
	file, err := execution.open("runtime.log")
	if err != nil {
		return err
	}
	output, err := execution.defaultClient.ContainerLogs(observation, execution.acceptedID, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true,
	})
	execution.operation("container logs", execution.acceptedID, err)
	if err != nil {
		return macRunnerErrors("retrieve final runtime output", macRunnerFailure("retrieve final logs for runner "+execution.acceptedID, err), file.Close())
	}
	_, copyErr := stdcopy.StdCopy(file, file, output)
	var terminalErr error
	if execution.terminal == nil || execution.terminal.State == nil || execution.terminal.State.Running {
		terminalErr = errors.New("final logs require an inspected stopped runner")
	}
	err = macRunnerErrors("capture final Docker logs", copyErr, output.Close(), file.Close(), observation.Err(), terminalErr)
	if err != nil {
		return macRunnerFailure("capture final logs for runner "+execution.acceptedID, err)
	}
	return nil
}
