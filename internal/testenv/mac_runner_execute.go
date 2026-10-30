package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"goodkind.io/tack/internal/clock"
)

func executeMacRunner(ctx, caller context.Context, prepared *macRunnerPreparation, preparationError error) (MacTestResult, error) {
	var state macRunnerExecution
	execution := &state
	execution.prepared = prepared
	execution.baseline = macRunnerBaseline{Containers: make(map[string]bool), Networks: make(map[string]bool), Volumes: make(map[string]bool)}
	execution.preserved = macRunnerBaseline{Containers: make(map[string]bool), Networks: make(map[string]bool), Volumes: make(map[string]bool)}
	execution.anonymousVolumes, execution.cacheVolumes = make(map[string]bool), make(map[string]bool)
	execution.temporaryVolumes = make(map[string]macRunnerTemporaryVolume)
	execution.startedAt = clock.Now().UTC()
	execution.write("wrapper.pid", strconv.Itoa(os.Getpid())+"\n")
	primary := execution.stage("source preparation", preparationError)
	if primary == nil {
		primary = execution.prepareExecution(ctx)
	}
	if primary == nil {
		primary = execution.stage("runner runtime", execution.run(ctx))
	}
	err := execution.finalize(ctx, primary)
	if err != nil {
		slog.ErrorContext(caller, "testenv.run_mac.failed", slog.String("err", err.Error()))
	}
	return execution.result(caller, err)
}

func (execution *macRunnerExecution) prepareExecution(ctx context.Context) error {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return macRunnerFailure("generate runner identity", fmt.Errorf("read random suffix: %w", err))
	}
	execution.name = "tack-mac-runner-" + hex.EncodeToString(suffix[:])
	execution.labels = map[string]string{
		macRunnerLabel: execution.name, macRunnerLabel + ".native": "true",
		macRunnerLabel + ".image": execution.prepared.options.Image, macRunnerLabel + ".root": execution.prepared.root,
		macRunnerProjectLabel: execution.prepared.project.Name, macRunnerServiceLabel: "tests",
	}
	if err := execution.stage("daemon and image inspection", execution.connect(ctx)); err != nil {
		return err
	}
	if err := execution.stage("baseline snapshot", execution.snapshot(ctx)); err != nil {
		return err
	}
	if err := execution.stage("event subscription", execution.observeEvents(context.WithoutCancel(ctx))); err != nil {
		return err
	}
	if err := execution.stage("default network", execution.prepareNetwork(ctx)); err != nil {
		return err
	}
	return execution.stage("runner creation", execution.create(ctx))
}
