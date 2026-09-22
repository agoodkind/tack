package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/telemetry"
)

type searchProvisionInput struct{ clispec.InputMarker }

func searchProvisionOp(f *cli.Factory) clispec.Operation[searchProvisionInput] {
	return clispec.Operation[searchProvisionInput]{
		Name: clispec.Name{Canonical: "provision", CLIOverride: ""}, Lifetime: clispec.Permanent,
		Audit: audit.Spec{Verb: string(audit.VerbOpsDeployVerify)}, Group: searchGroup,
		Short: "Create the empty native OpenSearch index", New: func() searchProvisionInput { return searchProvisionInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchProvisionInput, _ clispec.ResultSink) error {
			adapter, err := search.New(ctx, search.Config{Endpoint: f.Cfg.SearchEndpoint, CA: f.Cfg.SearchCA, Username: f.Cfg.SearchUsername, Password: f.Cfg.SearchPassword, RequestTimeout: f.Cfg.SearchRequestTimeout, MaxRetries: f.Cfg.SearchMaxRetries})
			if err != nil {
				wrapped := fmt.Errorf("create search adapter: %w", err)
				telemetry.L(ctx).ErrorContext(ctx, "search.provision.adapter_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			defer func() { _ = adapter.Close(ctx) }()
			model, err := adapter.Provision(ctx)
			if err != nil {
				wrapped := fmt.Errorf("provision search model: %w", err)
				telemetry.L(ctx).ErrorContext(ctx, "search.provision.model_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			if err := adapter.CreateIndex(ctx, "node-pages-1", search.IndexSpec{Model: model, MappingVersion: "1", Primaries: 1, RoutingShards: 1, Replicas: 0}); err != nil {
				wrapped := fmt.Errorf("create search index: %w", err)
				telemetry.L(ctx).ErrorContext(ctx, "search.provision.index_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			return adapter.SetReplicas(ctx, "node-pages-1", 0)
		},
	}
}
