package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	provisionIndex              = "node-pages-1"
	searchProjectionMissingCode = "search_projection_missing"
)

func searchProvisionCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "provision", Short: "Create an empty OpenSearch index.", Args: cobra.NoArgs}
	spec := externalSpec(audit.VerbOpsSearchProvision, "opensearch/serving-index")
	clispec.AttachExternal(cmd, f, spec, emptyCommandInput,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runSearchProvisionRecorded(ctx, f, steps)
		})
	return cmd
}

func runSearchProvisionRecorded(ctx context.Context, f *cli.Factory, steps audit.StepRecorder) (_ audit.OutcomeDetails, runErr error) {
	if err := requireSearchProjections(ctx, f); err != nil {
		return commandOutcome(audit.OutcomeRefused, searchProjectionMissingCode, err), err
	}
	topology, err := config.LoadSearchTopology(ctx)
	if err != nil {
		wrapped := searchProvisionFailure(ctx, "load search topology", err)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, wrapped), wrapped
	}
	adapter, err := newSearchMaintenanceAdapter(ctx, f.Cfg)
	if err != nil {
		wrapped := searchProvisionFailure(ctx, "create search adapter", err)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, wrapped), wrapped
	}
	defer func() {
		if err := adapter.Close(ctx); err != nil && runErr == nil {
			runErr = searchProvisionFailure(ctx, "close search adapter", err)
		}
	}()
	if err := verifySearchServer(ctx, adapter); err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	return provisionSearchSteps(ctx, f, adapter, topology, steps)
}

func searchProvisionFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search provision: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.provision.command_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
