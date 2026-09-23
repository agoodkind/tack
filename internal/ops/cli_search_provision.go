package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

type searchProvisionInput struct{ clispec.InputMarker }

func searchProvisionOp(f *cli.Factory) clispec.Operation[searchProvisionInput] {
	return clispec.Operation[searchProvisionInput]{
		Name: clispec.Name{Canonical: "provision", CLIOverride: ""}, Lifetime: clispec.Permanent,
		Audit: audit.Spec{Verb: string(audit.VerbOpsSearchProvision), Mutates: true}, Group: searchGroup,
		Short: "Create the empty native OpenSearch index", New: func() searchProvisionInput { return searchProvisionInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchProvisionInput, _ clispec.ResultSink) (runErr error) {
			if err := requireSearchProjections(ctx, f); err != nil {
				return err
			}
			logger := telemetry.L(ctx)
			topology, err := config.LoadSearchTopology(ctx)
			if err != nil {
				wrapped := fmt.Errorf("load search topology: %w", err)
				logger.ErrorContext(ctx, "search.provision.topology_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			certificate, err := config.LoadSearchCA(ctx, f.Cfg.SearchCA)
			if err != nil {
				wrapped := fmt.Errorf("load search CA: %w", err)
				logger.ErrorContext(ctx, "search.provision.ca_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			pass := f.Cfg.SearchPassword
			adapter, err := search.New(ctx, search.Config{Endpoint: f.Cfg.SearchEndpoint, CA: certificate, Username: f.Cfg.SearchUsername, Password: pass, RequestTimeout: f.Cfg.SearchRequestTimeout, MaxRetries: f.Cfg.SearchMaxRetries})
			if err != nil {
				wrapped := fmt.Errorf("create search adapter: %w", err)
				logger.ErrorContext(ctx, "search.provision.adapter_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			defer func() {
				if err := adapter.Close(ctx); err != nil && runErr == nil {
					runErr = fmt.Errorf("close search adapter: %w", err)
				}
			}()
			if err := verifySearchServer(ctx, adapter); err != nil {
				return err
			}
			model, err := adapter.Provision(ctx)
			if err != nil {
				wrapped := fmt.Errorf("provision search model: %w", err)
				logger.ErrorContext(ctx, "search.provision.model_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			const index = "node-pages-1"
			if err := adapter.EnsureIndex(ctx, index, search.IndexSpec{Model: model, MappingVersion: "1", Primaries: topology.Primaries, RoutingShards: topology.RoutingShards, Replicas: topology.Replicas}); err != nil {
				wrapped := fmt.Errorf("ensure search index %s: %w", index, err)
				logger.ErrorContext(ctx, "search.provision.index_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
				return wrapped
			}
			if err := adapter.WaitGreen(ctx, index); err != nil {
				wrapped := fmt.Errorf("wait for green search index %s: %w", index, err)
				logger.ErrorContext(ctx, "search.provision.health_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
				return wrapped
			}
			if err := adapter.SetAlias(ctx, "node-pages", index); err != nil {
				wrapped := fmt.Errorf("set search alias node-pages: %w", err)
				logger.ErrorContext(ctx, "search.provision.alias_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
				return wrapped
			}
			return recordServingSearchIndex(ctx, f, index)
		},
	}
}
