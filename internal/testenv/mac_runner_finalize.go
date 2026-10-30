package testenv

import (
	"context"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/moby/moby/client"
)

func (execution *macRunnerExecution) finalize(ctx context.Context, primary error) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	var failures []error
	if execution.acceptedID != "" {
		inspection, err := execution.inspect(cleanupContext, execution.acceptedID)
		if err == nil {
			err = execution.stopRunner(cleanupContext, inspection)
		}
		failures = append(failures, execution.stage("runner stop", err))
		terminal, terminalErr := execution.inspect(cleanupContext, execution.acceptedID)
		if terminalErr == nil {
			execution.terminal = &terminal
			writeMacRunnerJSON(execution, "outer-terminal.json", terminal)
		}
		failures = append(failures, execution.stage("terminal inspection", terminalErr))
	}
	failures = append(failures, execution.finishAttach())
	if execution.defaultClient != nil && execution.baselineReady {
		cleanupErr := execution.cleanup(cleanupContext)
		execution.status("cleanup.exit", cleanupErr)
		failures = append(failures, execution.stage("outer cleanup", cleanupErr))
	}
	failures = append(failures, execution.finishEvents())
	sourceErr := execution.prepared.verifySources()
	if !execution.prepared.sourceCaptured {
		sourceErr = errors.New("source capture did not complete; source verification remains unavailable")
	}
	execution.status("source-hash-verification.exit", sourceErr)
	failures = append(failures, execution.stage("source verification", sourceErr))
	for _, docker := range []*client.Client{execution.defaultClient, execution.rawClient} {
		if docker != nil {
			failures = append(failures, docker.Close())
		}
	}
	primaryCode := 1
	if primary == nil && execution.runtimeExit != nil {
		primaryCode = *execution.runtimeExit
	}
	execution.write("primary.exit", strconv.Itoa(primaryCode)+"\n")
	return macRunnerErrors("finalize invocation", primary, macRunnerErrors("finalize stages", failures...), macRunnerErrors("finalize evidence", execution.evidenceErrors...))
}

func (execution *macRunnerExecution) finishAttach() error {
	var failures []error
	if execution.attach != nil {
		if execution.started {
			err := macRunnerAwait(execution.attachDone, "attached runtime output")
			failures = append(failures, err)
			if !errors.Is(err, errMacRunnerStreamTimeout) {
				execution.attachDone = nil
			}
		}
		execution.attach.Close()
	}
	if execution.attachCancel != nil {
		execution.attachCancel()
	}
	if execution.attachDone != nil {
		failures = append(failures, macRunnerAwait(execution.attachDone, "attached runtime output after close"))
		execution.attachDone = nil
	}
	if execution.logFile != nil {
		failures = append(failures, execution.logFile.Close())
	}
	if execution.attach == nil {
		return macRunnerErrors("close runtime output", failures...)
	}
	return execution.stage("attach completion", macRunnerErrors("complete runtime output", failures...))
}

func (execution *macRunnerExecution) finishEvents() error {
	var failures []error
	if execution.eventCancel != nil {
		execution.eventCancel()
	}
	if execution.eventDone != nil {
		failures = append(failures, execution.stage("event completion", macRunnerAwait(execution.eventDone, "container event observer")))
		execution.eventDone = nil
	}
	for _, file := range []*os.File{execution.eventsFile, execution.eventsErrorFile} {
		if file != nil {
			failures = append(failures, file.Close())
		}
	}
	return macRunnerErrors("complete event evidence", failures...)
}

func (execution *macRunnerExecution) result(ctx context.Context, err error) (MacTestResult, error) {
	code := 1
	if err == nil && execution.runtimeExit != nil {
		code = *execution.runtimeExit
	}
	interrupted := errors.Is(ctx.Err(), context.Canceled)
	if interrupted {
		code = 130
	}
	evidenceErr := execution.summarize(code, interrupted, err)
	if evidenceErr != nil {
		err = macRunnerErrors("final evidence", err, evidenceErr)
		if !interrupted {
			code = 1
		}
		execution.write("wrapper.exit", strconv.Itoa(code)+"\n")
	}
	return MacTestResult{ExitCode: code, EvidenceDirectory: execution.prepared.options.EvidenceDirectory, RunnerID: execution.runnerID()}, err
}
