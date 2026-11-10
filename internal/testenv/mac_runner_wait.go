package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type macRunnerWaitOutcome struct {
	Response *container.WaitResponse
	Error    error
}

type macRunnerWaitEvidence struct {
	Response         *container.WaitResponse `json:"response,omitempty"`
	Error            string                  `json:"error,omitempty"`
	ReceiverComplete bool                    `json:"receiver_complete"`
}

func (execution *macRunnerExecution) observeWait(ctx context.Context) {
	observation, cancel := context.WithCancel(ctx)
	execution.waitCancel = cancel
	timer := time.AfterFunc(macRunnerObservation, cancel)
	stream := execution.defaultClient.ContainerWait(observation, execution.acceptedID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	timer.Stop()
	outcome := make(chan macRunnerWaitOutcome, 1)
	completed := make(chan error, 1)
	execution.waitOutcome, execution.waitReceiverDone = outcome, completed
	go func() {
		var receiverError error
		defer func() {
			if recovered := recover(); recovered != nil {
				receiverError = fmt.Errorf("container wait receiver panic: %v", recovered)
				slog.Error("testenv.run_mac.wait_failed", slog.String("err", receiverError.Error()))
				outcome <- macRunnerWaitOutcome{Response: nil, Error: receiverError}
			}
			completed <- receiverError
		}()
		select {
		case response := <-stream.Result:
			outcome <- macRunnerWaitOutcome{Response: &response, Error: nil}
		case err := <-stream.Error:
			outcome <- macRunnerWaitOutcome{Response: nil, Error: err}
		}
	}()
}

func (execution *macRunnerExecution) recordWaitOutcome(outcome macRunnerWaitOutcome) error {
	execution.waitEvidence = &macRunnerWaitEvidence{Response: outcome.Response, Error: "", ReceiverComplete: false}
	if outcome.Error != nil {
		execution.waitEvidence.Error = outcome.Error.Error()
		return macRunnerFailure("wait runner "+execution.acceptedID, outcome.Error)
	}
	if outcome.Response == nil {
		err := errors.New("container wait did not provide a response or error")
		execution.waitEvidence.Error = err.Error()
		return err
	}
	if outcome.Response.Error != nil && outcome.Response.Error.Message != "" {
		err := fmt.Errorf("wait runner %s: %s", execution.acceptedID, outcome.Response.Error.Message)
		execution.waitEvidence.Error = err.Error()
		return err
	}
	return nil
}

func (execution *macRunnerExecution) finishWait(ctx context.Context) error {
	if execution.waitReceiverDone == nil {
		return nil
	}
	execution.waitCancel()
	completionError := execution.stage("wait receiver completion", macRunnerAwait(execution.waitReceiverDone, "Docker wait receiver"))
	if completionError != nil {
		return completionError
	}
	var outcomeError error
	if execution.waitEvidence == nil {
		select {
		case outcome := <-execution.waitOutcome:
			outcomeError = execution.recordWaitOutcome(outcome)
		default:
			return errors.New("completed Docker wait receiver did not provide its outcome")
		}
	}
	execution.waitEvidence.ReceiverComplete = true
	writeMacRunnerJSON(execution, "wait.json", *execution.waitEvidence)
	if execution.runtimeExit == nil && execution.waitEvidence.Response != nil && execution.waitEvidence.Error == "" {
		outcomeError = execution.acceptTerminal(ctx, int(execution.waitEvidence.Response.StatusCode))
	}
	return macRunnerErrors("finalize Docker wait outcome", outcomeError)
}
