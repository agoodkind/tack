package ops

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

var errSearchMixedRebuildRunning = errors.New("qa datagen search-mixed: another index rebuild is already in progress")

// searchMixedRebuild validates a full rebuild with planSearchReindex and refuses an active replacement.
// It calls BeginRebuild and returns the target index. The search-mixed command records the run
// under its own audit event without a separate external step for the rebuild.
func searchMixedRebuild(factory *cli.Factory) datagen.SearchMixedRebuild {
	return func(ctx context.Context) (string, error) {
		env, err := NewEnv(ctx, factory.Cfg)
		if err != nil {
			return "", searchReindexFailure(ctx, "open search reindex environment", err)
		}
		defer env.Close()
		input := searchReindexInput{Mode: string(searchdomain.ReplacementFull), Primaries: 0, Restored: false}
		plan, err := planSearchReindex(ctx, factory, env, input)
		if err != nil {
			return "", err
		}
		if plan.running {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_rebuild_running", slog.String("err", errSearchMixedRebuildRunning.Error()),
				slog.String("rebuild_id", plan.current.ID.String()))
			return "", errSearchMixedRebuildRunning
		}
		rebuild, err := env.Stores.SearchRebuilds(opsClockSource{}).BeginRebuild(ctx, plan.request)
		if err != nil {
			return "", searchReindexFailure(ctx, "begin index replacement", err)
		}
		return rebuild.TargetIndex, nil
	}
}

func parseSearchMixedEndpoints(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	endpoints := make([]string, 0)
	for _, endpoint := range strings.Split(value, ",") {
		endpoint = strings.TrimSpace(endpoint)
		if err := validateDatagenSearchEndpoint(endpoint); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, nil
}
