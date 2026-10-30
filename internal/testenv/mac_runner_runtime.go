package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/clock"
)

func (execution *macRunnerExecution) run(ctx context.Context) error {
	if err := execution.attachRunner(ctx); err != nil {
		return err
	}
	createdDeadline := clock.Now().Add(30 * time.Second)
	_, err := macObserve(ctx, func(observation context.Context) (client.ContainerStartResult, error) {
		return execution.rawClient.ContainerStart(observation, execution.acceptedID, client.ContainerStartOptions{})
	})
	execution.operation("container start", execution.acceptedID, err)
	if err != nil {
		return macRunnerFailure("start runner "+execution.acceptedID, err)
	}
	execution.started = true
	waitContext, waitCancel := context.WithCancel(ctx)
	defer waitCancel()
	waitTimer := time.AfterFunc(macRunnerObservation, waitCancel)
	waited := execution.defaultClient.ContainerWait(waitContext, execution.acceptedID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	waitTimer.Stop()
	states, err := execution.open("states.jsonl")
	if err != nil {
		return err
	}
	defer func() { execution.evidenceErrors = append(execution.evidenceErrors, states.Close()) }()
	encoder := json.NewEncoder(states)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	if err := execution.observeState(ctx, encoder, createdDeadline); err != nil {
		return err
	}
	for {
		select {
		case result := <-waited.Result:
			if result.Error != nil && result.Error.Message != "" {
				return fmt.Errorf("wait runner %s: %s", execution.acceptedID, result.Error.Message)
			}
			return execution.acceptTerminal(ctx, int(result.StatusCode))
		case err := <-waited.Error:
			return macRunnerFailure("wait runner "+execution.acceptedID, err)
		case <-ticker.C:
			if err := execution.observeState(ctx, encoder, createdDeadline); err != nil {
				return err
			}
		case err := <-execution.eventDone:
			execution.eventDone = nil
			return macRunnerErrors("observe runner events", errors.New("container event observer stopped during runtime"), err)
		case <-ctx.Done():
			return macRunnerFailure("observe runner runtime", ctx.Err())
		}
	}
}

func (execution *macRunnerExecution) attachRunner(ctx context.Context) error {
	var err error
	execution.logFile, err = execution.open("runtime.log")
	if err != nil {
		return err
	}
	attachment, cancel := context.WithCancel(context.WithoutCancel(ctx))
	execution.attachCancel = cancel
	timer := time.AfterFunc(macRunnerObservation, cancel)
	attached, err := execution.rawClient.ContainerAttach(attachment, execution.acceptedID, client.ContainerAttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	timer.Stop()
	if err != nil {
		return macRunnerFailure("attach runner "+execution.acceptedID, err)
	}
	execution.attach = &attached
	execution.attachDone = make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("runtime output observer panic: %v", recovered)
				slog.Error("testenv.run_mac.attach_failed", slog.String("err", err.Error()))
				execution.attachDone <- err
			}
		}()
		_, err := stdcopy.StdCopy(execution.logFile, execution.logFile, attached.Reader)
		execution.attachDone <- err
	}()
	return nil
}

func (execution *macRunnerExecution) observeState(ctx context.Context, encoder *json.Encoder, createdDeadline time.Time) error {
	inspection, err := execution.inspect(ctx, execution.acceptedID)
	if err != nil {
		return err
	}
	if inspection.State == nil {
		return errors.New("runner state observation did not report state")
	}
	processes, processErr := macObserve(ctx, func(observation context.Context) (client.ContainerTopResult, error) {
		return execution.defaultClient.ContainerTop(observation, execution.acceptedID, client.ContainerTopOptions{})
	})
	state := struct {
		Time         time.Time                  `json:"time"`
		State        *container.State           `json:"state"`
		Processes    *client.ContainerTopResult `json:"processes,omitempty"`
		ProcessError string                     `json:"process_error,omitempty"`
	}{Time: clock.Now().UTC(), State: inspection.State, Processes: nil, ProcessError: ""}
	if processErr == nil {
		state.Processes = &processes
	} else {
		state.ProcessError = processErr.Error()
	}
	if err := encoder.Encode(state); err != nil {
		return macRunnerFailure("write runner state", err)
	}
	if inspection.State.Status == container.StateCreated && !clock.Now().Before(createdDeadline) {
		return errors.New("runner remained Created for 30 seconds after startup")
	}
	return nil
}

func (execution *macRunnerExecution) acceptTerminal(ctx context.Context, waitedCode int) error {
	inspection, err := execution.inspect(ctx, execution.acceptedID)
	if err != nil {
		return err
	}
	execution.terminal = &inspection
	writeMacRunnerJSON(execution, "outer-terminal.json", inspection)
	if inspection.State == nil || inspection.State.Running || inspection.State.Status != container.StateExited || inspection.State.ExitCode != waitedCode {
		return errors.New("runner final inspection does not agree with ContainerWait status")
	}
	execution.runtimeExit = &waitedCode
	return nil
}
