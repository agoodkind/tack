package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

// provisionSeedDeferredCode is the error code of a provision run that
// completed every other step and deferred the product seed.
const provisionSeedDeferredCode = "seed_deferred"

// errProvisionSeedDeferred reports that the app container could not run the
// product seed.
var errProvisionSeedDeferred = errors.New("provision: the app container could not run the product seed; run provision again after the app is running")

// provisionEvidence is the planned and observed state of one provision step.
type provisionEvidence struct {
	Step         string `json:"step"`
	Redundancy   string `json:"redundancy,omitempty"`
	AllowFDBInit bool   `json:"allow_fdb_init,omitempty"`
	Container    string `json:"container,omitempty"`
	State        string `json:"state,omitempty"`
}

func (provisionEvidence) isStepEvidence() {}

// runProvisionRecorded runs the ordered first-boot steps and records each
// step: the FoundationDB configuration, the migrations, the audit roles, the
// FoundationDB continuous backup, and the product seed. It stops at the
// first failed step. It returns an incomplete outcome when the app container
// could not run the product seed.
func runProvisionRecorded(
	ctx context.Context,
	cfg *config.Config,
	allowFDBInit bool,
	sink clispec.ResultSink,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	cli, err := newDockerClient(ctx)
	if err != nil {
		wrapped := fmt.Errorf("provision docker client: %w", err)
		slog.ErrorContext(ctx, "provision.docker_client.failed", slog.String("err", wrapped.Error()))
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	defer func() { _ = cli.Close() }()
	fdbEvidence := provisionEvidence{Step: "fdb", Redundancy: cfg.OpsFDBRedundancyMode, AllowFDBInit: allowFDBInit, Container: "", State: ""}
	if details, err := provisionStateStep(ctx, steps, "foundationdb/cluster", fdbEvidence, func(ctx context.Context) (string, error) {
		return provisionFDB(ctx, cli, cfg, allowFDBInit)
	}); err != nil {
		return details, err
	}
	if _, details, err := schemastatus.ApplyRecorded(ctx, cfg.DatabaseURL, migrations.FS, schemastatus.Guard{ExpectFrom: 0, To: 0}, steps); err != nil {
		return details, fmt.Errorf("provision migrate: %w", err)
	}
	if details, err := runAuditSeedRolesRecorded(ctx, cfg, steps); err != nil {
		return details, fmt.Errorf("provision seed roles: %w", err)
	}
	if cfg.BackupFDBContinuous {
		if details, err := runBackupFDBContinuousInitRecorded(ctx, cfg, steps); err != nil {
			return details, fmt.Errorf("provision fdb continuous backup: %w", err)
		}
	}
	if cfg.SeedEmail == "" || cfg.SeedName == "" {
		slog.InfoContext(ctx, "provision.seed.skipped",
			slog.String("reason", "SEED_EMAIL and SEED_NAME unset; product seed is left to a deliberate run"))
		return provisionResult(ctx, sink, "provision complete", commandOutcome(audit.OutcomeCompleted, "", nil))
	}
	return provisionSeedRecorded(ctx, cli, cfg, sink, steps)
}

// provisionSeedRecorded records the product seed as one step. A deferred
// seed returns an incomplete outcome.
func provisionSeedRecorded(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	sink clispec.ResultSink,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	var state string
	seedEvidence := provisionEvidence{Step: "seed", Redundancy: "", AllowFDBInit: false, Container: cfg.OpsAppContainer, State: ""}
	details, err := provisionStateStep(ctx, steps, "docker/"+cfg.OpsAppContainer+"/seed", seedEvidence, func(ctx context.Context) (string, error) {
		observed, seedErr := provisionSeed(ctx, cli, cfg)
		state = observed
		return observed, seedErr
	})
	if err != nil {
		return details, err
	}
	if state == provisionSeedDeferred {
		return provisionResult(ctx, sink, errProvisionSeedDeferred.Error(),
			commandOutcome(audit.OutcomeIncomplete, provisionSeedDeferredCode, errProvisionSeedDeferred))
	}
	return provisionResult(ctx, sink, "provision complete", commandOutcome(audit.OutcomeCompleted, "", nil))
}

// provisionStateStep records one step on reference. The step result records
// the state that effect returns.
func provisionStateStep(
	ctx context.Context,
	steps audit.StepRecorder,
	reference string,
	planned provisionEvidence,
	effect func(context.Context) (string, error),
) (audit.OutcomeDetails, error) {
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	var state string
	return recordedStep(ctx, steps, audit.ExternalStep(externalTarget(reference), plannedJSON),
		func(ctx context.Context) error {
			observed, effectErr := effect(ctx)
			state = observed
			return effectErr
		},
		func(ctx context.Context) (json.RawMessage, error) {
			observed := planned
			observed.State = state
			return encodeEvidence(ctx, observed)
		})
}

// provisionResult writes message to sink and returns details.
func provisionResult(ctx context.Context, sink clispec.ResultSink, message string, details audit.OutcomeDetails) (audit.OutcomeDetails, error) {
	if err := sink.WriteText(ctx, message); err != nil {
		wrapped := fmt.Errorf("provision: write the result: %w", err)
		slog.ErrorContext(ctx, "provision.output_failed", slog.String("err", wrapped.Error()))
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, wrapped), wrapped
	}
	return details, nil
}
