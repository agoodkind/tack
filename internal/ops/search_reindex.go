package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

type searchReindexInput struct {
	Mode      string `json:"mode"`
	Primaries int    `json:"primaries"`
	Restored  bool   `json:"restored"`
}

func (searchReindexInput) isCommandInput() {}

type searchReindexOutput struct {
	clispec.ResultMarker
	RebuildID   string `json:"rebuild_id"`
	Mode        string `json:"mode"`
	State       string `json:"state"`
	SourceIndex string `json:"source_index"`
	TargetIndex string `json:"target_index"`
	Primaries   int    `json:"primary_shards"`
	Resumed     bool   `json:"resumed"`
	DryRun      bool   `json:"dry_run"`
}

// searchReindexCommand builds `ops search reindex`. With --execute, the
// command records any replacement it begins as an external step on the
// serving index. Without --execute, its preview prints the replacement in
// progress or the planned replacement.
func searchReindexCommand(f *cli.Factory) *cobra.Command {
	in := searchReindexInput{Mode: "full", Primaries: 0, Restored: false}
	cmd := &cobra.Command{
		Use: "reindex", Short: "Replace the serving search index by a full FoundationDB rebuild or a native split",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&in.Mode, "mode", "full", "This parameter selects full for a FoundationDB rebuild or split for a native split.")
	cmd.Flags().IntVar(&in.Primaries, "primaries", 0, "This parameter sets the primary shard count of the split target.")
	cmd.Flags().BoolVar(&in.Restored, "restored", false, "This flag marks a rebuild after a FoundationDB restore. The rebuild rejects every existing search cursor.")
	spec := externalSpec(audit.VerbOpsSearchReindex, "opensearch/serving-index")
	input := func() (json.RawMessage, error) { return encodeCommandInput(in) }
	clispec.AttachExternalPreview(cmd, f, spec, input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runSearchReindexRecorded(ctx, f, in, clispec.NewCLISink(f), steps)
		},
		func(ctx context.Context) error {
			return runSearchReindex(ctx, f, in, clispec.NewCLISink(f))
		})
	return cmd
}

// runSearchReindex prints the replacement in progress or the planned
// replacement. If a replacement is not in progress, it requires valid search
// declarations and a default projection for each property definition without
// a declaration, and it validates the request against the serving index. It
// does not write to the search engine.
func runSearchReindex(ctx context.Context, f *cli.Factory, in searchReindexInput, sink clispec.ResultSink) error {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return searchReindexFailure(ctx, "open search reindex environment", err)
	}
	defer env.Close()
	plan, err := planSearchReindex(ctx, f, env, in)
	if err != nil {
		return err
	}
	if plan.running {
		return writeReindexResult(ctx, sink, plan.current, true, true)
	}
	planned := searchdomain.Rebuild{
		ID: uuid.Nil, Mode: plan.request.Mode, State: "", SourceIndex: plan.serving, TargetIndex: "",
		ScanCursor: "", VerifyCursor: "", Failure: "", PrimaryShards: plan.request.PrimaryShards,
		RoutingShards: plan.request.RoutingShards, Replicas: plan.request.Replicas, ScanComplete: false,
		Paused: false, Restored: plan.request.Restored, PausedAt: time.Time{}, Generation: 0,
	}
	return writeReindexResult(ctx, sink, planned, false, true)
}

// reindexPlan is the replacement in progress, or the serving index and the
// validated request of a new replacement.
type reindexPlan struct {
	current searchdomain.Rebuild
	running bool
	serving string
	request searchdomain.BeginRebuild
}

// planSearchReindex reads the replacement in progress. Without one, it
// requires valid search declarations and a default projection for each
// property definition without a declaration, reads the serving index, and
// validates the request before any engine write.
func planSearchReindex(ctx context.Context, f *cli.Factory, env *Env, in searchReindexInput) (reindexPlan, error) {
	var plan reindexPlan
	current, running, err := env.Stores.SearchRebuilds(opsClockSource{}).CurrentRebuild(ctx)
	if err != nil {
		return plan, searchReindexFailure(ctx, "read index replacement", err)
	}
	plan.current, plan.running = current, running
	if running {
		return plan, nil
	}
	if err := requireSearchProjections(ctx, f); err != nil {
		return plan, err
	}
	serving, err := env.Stores.ServingSearchIndex(ctx)
	if err != nil {
		return plan, searchReindexFailure(ctx, "read serving search index", err)
	}
	request, err := reindexRequest(ctx, f, in, serving)
	if err != nil {
		return plan, err
	}
	plan.serving, plan.request = serving, request
	return plan, nil
}

// reindexRequest builds the replacement request and validates it with
// [searchdomain.BeginRebuild.Validate].
func reindexRequest(ctx context.Context, f *cli.Factory, in searchReindexInput, serving string) (searchdomain.BeginRebuild, error) {
	request, err := reindexTopology(ctx, f, in, serving)
	if err != nil {
		return searchdomain.BeginRebuild{}, err
	}
	if err := request.Validate(); err != nil {
		return searchdomain.BeginRebuild{}, searchReindexFailure(ctx, "validate index replacement", err)
	}
	return request, nil
}

// reindexTopology sets the topology of the requested mode. A full
// replacement uses the configured topology. A split keeps the serving
// routing and replica counts and requires a permitted primary count. An
// unknown mode gets an empty topology.
func reindexTopology(ctx context.Context, f *cli.Factory, in searchReindexInput, serving string) (searchdomain.BeginRebuild, error) {
	request := searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementMode(in.Mode), PrimaryShards: 0, RoutingShards: 0,
		Replicas: 0, Restored: in.Restored, Reason: "operator request",
	}
	switch request.Mode {
	case searchdomain.ReplacementFull:
		topology, err := config.LoadSearchTopology(ctx)
		if err != nil {
			return request, searchReindexFailure(ctx, "load search topology", err)
		}
		request.PrimaryShards, request.RoutingShards, request.Replicas = topology.Primaries, topology.RoutingShards, topology.Replicas
	case searchdomain.ReplacementSplit:
		settings, err := validateSplit(ctx, f, serving, in.Primaries)
		if err != nil {
			return request, err
		}
		request.PrimaryShards, request.RoutingShards, request.Replicas = in.Primaries, settings.RoutingShards, settings.Replicas
	}
	return request, nil
}

func validateSplit(ctx context.Context, f *cli.Factory, serving string, primaries int) (settings search.PhysicalSettings, runErr error) {
	adapter, err := newSearchMaintenanceAdapter(ctx, f.Cfg)
	if err != nil {
		return settings, searchReindexFailure(ctx, "create search adapter", err)
	}
	defer func() {
		if closeErr := adapter.Close(ctx); closeErr != nil && runErr == nil {
			runErr = searchReindexFailure(ctx, "close search adapter", closeErr)
		}
	}()
	settings, err = adapter.ValidateSplit(ctx, serving, primaries)
	if err != nil {
		return settings, searchReindexFailure(ctx, "validate split of "+serving, err)
	}
	return settings, nil
}

func writeReindexResult(ctx context.Context, sink clispec.ResultSink, rebuild searchdomain.Rebuild, resumed, dryRun bool) error {
	output := searchReindexOutput{
		ResultMarker: clispec.ResultMarker{}, RebuildID: rebuild.ID.String(), Mode: string(rebuild.Mode),
		State: string(rebuild.State), SourceIndex: rebuild.SourceIndex, TargetIndex: rebuild.TargetIndex,
		Primaries: rebuild.PrimaryShards, Resumed: resumed, DryRun: dryRun,
	}
	if err := clispec.WriteJSONValue(ctx, sink, output); err != nil {
		return searchReindexFailure(ctx, "write index replacement result", err)
	}
	return nil
}

func searchReindexFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search reindex: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.reindex.command_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
