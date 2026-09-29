package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

func runSearchVerify(ctx context.Context, factory *cli.Factory) (runErr error) {
	logger := telemetry.L(ctx)
	topology, err := config.LoadSearchTopology(ctx)
	if err != nil {
		wrapped := fmt.Errorf("load search topology: %w", err)
		logger.ErrorContext(ctx, "search.verify.topology_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	certificate, err := config.LoadSearchCA(ctx, factory.Cfg.SearchCA)
	if err != nil {
		wrapped := fmt.Errorf("load search CA: %w", err)
		logger.ErrorContext(ctx, "search.verify.ca_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	pass := factory.Cfg.SearchPassword
	adapter, err := search.New(ctx, search.Config{
		Endpoint: factory.Cfg.SearchEndpoint, CA: certificate,
		Username: factory.Cfg.SearchUsername, Password: pass,
		RequestTimeout: factory.Cfg.SearchRequestTimeout, MaxRetries: factory.Cfg.SearchMaxRetries,
	})
	if err != nil {
		wrapped := fmt.Errorf("create search adapter: %w", err)
		logger.ErrorContext(ctx, "search.verify.adapter_failed", slog.String("err", wrapped.Error()))
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
	index, err := readServingSearchIndex(ctx, factory)
	if err != nil {
		return err
	}
	if err := verifySearchPhysical(ctx, adapter, topology, index); err != nil {
		return err
	}
	return verifySearchEndpoints(ctx, adapter, factory.Cfg.SearchEndpoint)
}

// verifySearchEndpoints requires the official client to have used only the
// configured stable endpoint.
func verifySearchEndpoints(ctx context.Context, adapter *search.Adapter, configured string) error {
	logger := telemetry.L(ctx)
	endpoints, err := adapter.Endpoints(ctx)
	if err != nil {
		wrapped := fmt.Errorf("read search client endpoints: %w", err)
		logger.ErrorContext(ctx, "search.verify.endpoints_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	want := strings.TrimRight(configured, "/")
	if len(endpoints) != 1 || endpoints[0] != want {
		wrapped := fmt.Errorf("search client used endpoints %q, want only %q", endpoints, want)
		logger.ErrorContext(ctx, "search.verify.endpoints_mismatch", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

func verifySearchServer(ctx context.Context, adapter *search.Adapter) error {
	logger := telemetry.L(ctx)
	if err := adapter.Ping(ctx); err != nil {
		wrapped := fmt.Errorf("verify search endpoint: %w", err)
		logger.ErrorContext(ctx, "search.verify.ping_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	version, err := adapter.ServerVersion(ctx)
	if err != nil {
		wrapped := fmt.Errorf("read search server version: %w", err)
		logger.ErrorContext(ctx, "search.verify.version_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if version != "3.8.0" {
		wrapped := fmt.Errorf("search server version %q does not match %q", version, "3.8.0")
		logger.ErrorContext(ctx, "search.verify.version_mismatch", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// verifySearchPhysical checks the mapping, model, topology, alias target, and
// health of index, the serving index that FoundationDB records.
func verifySearchPhysical(ctx context.Context, adapter *search.Adapter, topology config.SearchTopology, index string) error {
	logger := telemetry.L(ctx)
	info, err := adapter.IndexInfo(ctx, index)
	if err != nil {
		wrapped := fmt.Errorf("read search mapping %s: %w", index, err)
		logger.ErrorContext(ctx, "search.verify.mapping_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if info.MappingVersion != "1" {
		wrapped := fmt.Errorf("search mapping version %q does not match %q", info.MappingVersion, "1")
		logger.ErrorContext(ctx, "search.verify.mapping_mismatch", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if info.TokenizerSHA256 != search.PinnedModel.TokenizerDigest {
		wrapped := fmt.Errorf("search mapping tokenizer SHA-256 %q does not match %q", info.TokenizerSHA256, search.PinnedModel.TokenizerDigest)
		logger.ErrorContext(ctx, "search.verify.tokenizer_mismatch", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if err := adapter.VerifyModel(ctx, info.ModelID); err != nil {
		wrapped := fmt.Errorf("verify search model %s: %w", info.ModelID, err)
		logger.ErrorContext(ctx, "search.verify.model_failed", slog.String("err", wrapped.Error()), slog.String("model_id", info.ModelID))
		return wrapped
	}
	settings, err := adapter.IndexSettings(ctx, index)
	if err != nil {
		wrapped := fmt.Errorf("read search index settings %s: %w", index, err)
		logger.ErrorContext(ctx, "search.verify.settings_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if settings.Primaries != topology.Primaries || settings.RoutingShards != topology.RoutingShards || settings.Replicas != topology.Replicas {
		wrapped := fmt.Errorf("search topology %d/%d/%d does not match %d/%d/%d", settings.Primaries, settings.RoutingShards, settings.Replicas, topology.Primaries, topology.RoutingShards, topology.Replicas)
		logger.ErrorContext(ctx, "search.verify.topology_mismatch", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	model := search.PinnedModel
	model.ID = info.ModelID
	if err := adapter.VerifyIndex(ctx, index, search.IndexSpec{Model: model, MappingVersion: "1", Primaries: topology.Primaries, RoutingShards: topology.RoutingShards, Replicas: topology.Replicas}); err != nil {
		wrapped := fmt.Errorf("verify search index %s: %w", index, err)
		logger.ErrorContext(ctx, "search.verify.index_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	aliasTarget, err := adapter.AliasTarget(ctx, "node-pages")
	if err != nil {
		wrapped := fmt.Errorf("read search alias node-pages: %w", err)
		logger.ErrorContext(ctx, "search.verify.alias_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if aliasTarget != index {
		wrapped := fmt.Errorf("search alias node-pages points to %q, want the serving index %q that FoundationDB records", aliasTarget, index)
		logger.ErrorContext(ctx, "search.verify.alias_mismatch", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if err := adapter.WaitGreen(ctx, index, search.DefaultGreenWait); err != nil {
		wrapped := fmt.Errorf("verify green search index %s: %w", index, err)
		logger.ErrorContext(ctx, "search.verify.health_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}
