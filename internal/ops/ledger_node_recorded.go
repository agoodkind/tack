package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// ledgerNodeConfigTarget is the external target reference of the saved
// launcher config of the local ledger node.
const ledgerNodeConfigTarget = ledgerNodeTarget + "/launcher-config"

// ledgerNodeJSONFormat is the payload format of the node-prepare preview.
const ledgerNodeJSONFormat = "application/json"

// ledgerNodeWaitEvidence is the planned and observed state of the node wait.
type ledgerNodeWaitEvidence struct {
	Container     string `json:"container"`
	Stall         string `json:"stall"`
	Poll          string `json:"poll"`
	Health        string `json:"health,omitempty"`
	WaitedSeconds int64  `json:"waited_seconds,omitempty"`
}

func (ledgerNodeWaitEvidence) isStepEvidence() {}

// runLedgerNodePrepareRecorded records the node-prepare change as one step on
// the saved launcher config. The step intent records the observed state and
// planned change. The step result records the result after the change.
func runLedgerNodePrepareRecorded(ctx context.Context, cfg *config.Config, sink clispec.ResultSink, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	docker, err := newDockerClient(ctx)
	if err != nil {
		wrapped := fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	defer func() { _ = docker.Close() }()
	plan, err := planLedgerNodePrepare(ctx, cfg, docker)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	planned := plan.result
	planned.DryRun = false
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	applied := planned
	details, err := recordedStep(ctx, steps, audit.ExternalStep(externalTarget(ledgerNodeConfigTarget), plannedJSON),
		func(ctx context.Context) error {
			result, applyErr := applyLedgerNodePrepare(ctx, docker, plan)
			applied = result
			return applyErr
		},
		func(ctx context.Context) (json.RawMessage, error) { return encodeEvidence(ctx, applied) })
	if err != nil {
		return details, err
	}
	return details, writeLedgerNodePrepareResult(ctx, sink, applied)
}

// previewLedgerNodePrepare records the read of the node state through
// clispec.RunAuditedRead and prints the planned change after the read event
// is saved.
func previewLedgerNodePrepare(ctx context.Context, f *cli.Factory, sink clispec.ResultSink) error {
	input, err := emptyCommandInput()
	if err != nil {
		return err
	}
	payload, err := clispec.RunAuditedRead(ctx, f, readSpec(audit.VerbOpsLedgerNodePrepare, ledgerNodeTarget), input,
		func(ctx context.Context) (audit.OperatorReadOutput, error) {
			docker, err := newDockerClient(ctx)
			if err != nil {
				return audit.OperatorReadOutput{}, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
			}
			defer func() { _ = docker.Close() }()
			plan, err := planLedgerNodePrepare(ctx, f.Cfg, docker)
			if err != nil {
				return audit.OperatorReadOutput{}, err
			}
			encoded, err := json.Marshal(plan.result)
			if err != nil {
				slog.ErrorContext(ctx, "ops.ledger.node_prepare.encode_failed", slog.String("err", err.Error()))
				return audit.OperatorReadOutput{}, fmt.Errorf("%s: encode the result: %w", ledgerNodePrepareLabel, err)
			}
			return audit.OperatorReadOutput{
				Payload: encoded, Format: ledgerNodeJSONFormat, Targets: []audit.Target{externalTarget(ledgerNodeConfigTarget)},
			}, nil
		})
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.preview_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
	}
	if err := sink.WriteJSON(ctx, payload); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", ledgerNodePrepareLabel, err)
	}
	return nil
}

// runLedgerNodeWaitRecorded records the wait for the local ledger node as one
// step on its health. The step result records the observed health and the
// wait time. A stall or a stopped container fails the step.
func runLedgerNodeWaitRecorded(
	ctx context.Context,
	cfg *config.Config,
	in ledgerNodeWaitInput,
	sink clispec.ResultSink,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	const command = "ops ledger node-wait"
	stall, poll, err := ledgerNodeWaitWindows(in)
	if err != nil {
		wrapped := fmt.Errorf("%s: %w", command, err)
		slog.ErrorContext(ctx, "ops.ledger.node_wait.failed", slog.String("err", wrapped.Error()))
		return commandOutcome(audit.OutcomeRefused, invalidInputCode, wrapped), wrapped
	}
	docker, err := newDockerClient(ctx)
	if err != nil {
		wrapped := fmt.Errorf("%s: %w", command, err)
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	defer func() { _ = docker.Close() }()
	planned := ledgerNodeWaitEvidence{Container: yugabyteBackupContainer, Stall: stall.String(), Poll: poll.String(), Health: "", WaitedSeconds: 0}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	watch := ledgerNodeWatch{
		Health: func(ctx context.Context) (ledgerNodeHealth, error) {
			return inspectLedgerNodeHealth(ctx, docker, yugabyteBackupContainer)
		},
		Cluster: func(ctx context.Context) (ledgerClusterCounts, error) {
			return probeLedgerClusterCounts(ctx, cfg.BackupYBMasterAddresses)
		},
	}
	var took time.Duration
	details, err := recordedStep(ctx, steps, audit.ExternalStep(externalTarget(ledgerNodeTarget+"/health"), plannedJSON),
		func(ctx context.Context) error {
			waited, waitErr := awaitLedgerNode(ctx, watch, stall, poll)
			if waitErr != nil {
				slog.ErrorContext(ctx, "ops.ledger.node_wait.failed", slog.String("err", waitErr.Error()))
				return fmt.Errorf("%s: %w", command, waitErr)
			}
			took = waited
			return nil
		},
		func(ctx context.Context) (json.RawMessage, error) {
			observed := planned
			observed.Health, observed.WaitedSeconds = ledgerNodeHealthy, int64(took.Seconds())
			return encodeEvidence(ctx, observed)
		})
	if err != nil {
		return details, err
	}
	slog.InfoContext(ctx, "ops.ledger.node_wait.healthy", slog.Duration("took", took))
	line := fmt.Sprintf("%s: healthy after %s", yugabyteBackupContainer, took.Round(time.Second))
	if err := sink.WriteText(ctx, line); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_wait.write_result_failed", slog.String("err", err.Error()))
		return details, fmt.Errorf("%s: write result: %w", command, err)
	}
	return details, nil
}
