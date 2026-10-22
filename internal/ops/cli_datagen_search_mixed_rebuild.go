package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

var errSearchMixedRebuildRunning = errors.New("qa datagen search-mixed: another index rebuild is already in progress")

// searchMixedReindexSink keeps the result that the search reindex command writes.
type searchMixedReindexSink struct {
	output searchReindexOutput
}

func (s *searchMixedReindexSink) WriteJSON(ctx context.Context, payload json.RawMessage) error {
	if err := json.Unmarshal(payload, &s.output); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.search_mixed_reindex_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen search-mixed: the command could not decode the reindex result: %w", err)
	}
	return nil
}

func (s *searchMixedReindexSink) WriteText(context.Context, string) error {
	return nil
}

// searchMixedRebuild uses the search reindex command to begin a full rebuild and returns its
// target index. It refuses to measure a rebuild that is already in progress.
func searchMixedRebuild(factory *cli.Factory) datagen.SearchMixedRebuild {
	return func(ctx context.Context) (string, error) {
		sink := &searchMixedReindexSink{output: searchReindexOutput{}}
		input := searchReindexInput{
			InputMarker: clispec.InputMarker{}, Mode: string(searchdomain.ReplacementFull), Primaries: 0, Restored: false,
		}
		if err := runSearchReindex(ctx, factory, input, sink, false); err != nil {
			return "", err
		}
		if sink.output.Resumed {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_rebuild_running", slog.String("err", errSearchMixedRebuildRunning.Error()),
				slog.String("rebuild_id", sink.output.RebuildID))
			return "", errSearchMixedRebuildRunning
		}
		return sink.output.TargetIndex, nil
	}
}

// parseSearchMixedEndpoints splits a comma-separated endpoint list and validates each URL.
// It returns nil for an empty or whitespace-only string.
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
