package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

type searchReindexInput struct {
	clispec.InputMarker
	Mode      string
	Primaries int
	Restored  bool
}

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

func searchReindexOp(f *cli.Factory) clispec.Operation[searchReindexInput] {
	return clispec.Operation[searchReindexInput]{
		Name: clispec.Name{Canonical: "reindex", CLIOverride: ""}, Lifetime: clispec.Permanent,
		Audit: audit.Spec{Verb: string(audit.VerbOpsSearchReindex), Mutates: true}, Group: searchGroup,
		Short: "Replace the serving search index by a full FoundationDB rebuild or a native split",
		Params: []clispec.Param[searchReindexInput]{
			clispec.StringParam("mode", "This parameter selects full for a FoundationDB rebuild or split for a native split.", "full", false, func(in *searchReindexInput, value string) { in.Mode = value }),
			clispec.IntParam("primaries", "This parameter sets the primary shard count of the split target.", 0, func(in *searchReindexInput, value int) { in.Primaries = value }),
			clispec.BoolParam("restored", "This flag marks a rebuild after a FoundationDB restore. The rebuild rejects every existing search cursor.", false, func(in *searchReindexInput, value bool) { in.Restored = value }),
		},
		New: func() searchReindexInput {
			return searchReindexInput{InputMarker: clispec.InputMarker{}, Mode: "full", Primaries: 0, Restored: false}
		},
		DryRun: func(ctx context.Context, in searchReindexInput, sink clispec.ResultSink) error {
			return runSearchReindex(ctx, f, in, sink, true)
		},
		Run: func(ctx context.Context, in searchReindexInput, sink clispec.ResultSink) error {
			return runSearchReindex(ctx, f, in, sink, false)
		},
	}
}

// runSearchReindex returns the replacement in progress unchanged. A rerun
// after an interrupted command reports that replacement. Otherwise it requires
// every projection declaration, validates the request against the serving
// index before any engine write, and begins the replacement.
func runSearchReindex(ctx context.Context, f *cli.Factory, in searchReindexInput, sink clispec.ResultSink, dryRun bool) (runErr error) {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		return searchReindexFailure(ctx, "open search reindex environment", err)
	}
	defer env.Close()
	rebuilds := env.Stores.SearchRebuilds(opsClockSource{})
	current, running, err := rebuilds.CurrentRebuild(ctx)
	if err != nil {
		return searchReindexFailure(ctx, "read index replacement", err)
	}
	if running {
		return writeReindexResult(ctx, sink, current, true, dryRun)
	}
	if err := requireSearchProjections(ctx, f); err != nil {
		return err
	}
	serving, err := env.Stores.ServingSearchIndex(ctx)
	if err != nil {
		return searchReindexFailure(ctx, "read serving search index", err)
	}
	request, err := reindexRequest(ctx, f, in, serving)
	if err != nil {
		return err
	}
	if dryRun {
		planned := searchdomain.Rebuild{
			ID: uuid.Nil, Mode: request.Mode, State: "", SourceIndex: serving, TargetIndex: "",
			ScanCursor: "", VerifyCursor: "", Failure: "", PrimaryShards: request.PrimaryShards,
			RoutingShards: request.RoutingShards, Replicas: request.Replicas, ScanComplete: false,
			Paused: false, Restored: request.Restored, PausedAt: time.Time{}, Generation: 0,
		}
		return writeReindexResult(ctx, sink, planned, false, true)
	}
	rebuild, err := rebuilds.BeginRebuild(ctx, request)
	if err != nil {
		return searchReindexFailure(ctx, "begin index replacement", err)
	}
	return writeReindexResult(ctx, sink, rebuild, false, false)
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
	certificate, err := config.LoadSearchCA(ctx, f.Cfg.SearchCA)
	if err != nil {
		return settings, searchReindexFailure(ctx, "load search CA", err)
	}
	pass := f.Cfg.SearchPassword
	adapter, err := search.New(ctx, search.Config{
		Endpoint: f.Cfg.SearchEndpoint, CA: certificate, Username: f.Cfg.SearchUsername,
		Password: pass, RequestTimeout: f.Cfg.SearchRequestTimeout, MaxRetries: f.Cfg.SearchMaxRetries,
	})
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
