package ops

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

var (
	errSearchMixedRebuildRunning = errors.New("qa datagen search-mixed: another index rebuild is already in progress")
	errSearchMixedRebuildText    = errors.New("qa datagen search-mixed: the rebuild result sink does not accept text output")
)

// searchMixedRebuild returns a function that starts a full index rebuild with
// the same audited operation as ops search reindex --mode full --execute.
func searchMixedRebuild(factory *cli.Factory) datagen.SearchMixedRebuild {
	return func(ctx context.Context) (string, error) {
		input := searchReindexInput{Mode: string(searchdomain.ReplacementFull), Primaries: 0, Restored: false}
		encoded, err := encodeCommandInput(input)
		if err != nil {
			return "", searchReindexFailure(ctx, "the command could not encode the reindex input", err)
		}
		sink := &searchMixedRebuildSink{result: searchReindexOutput{}}
		err = clispec.RunAuditedExternal(ctx, factory.Out, externalSpec(audit.VerbOpsSearchReindex, "opensearch/serving-index"),
			encoded, factory.OperatorIdentitySource(), factory.DeployCommit(), factory.Execute(), factory.AuditOutbox(),
			factory.AuditInfrastructureProbe(), func(stepContext context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
				return runSearchReindexRecorded(stepContext, factory, input, sink, steps)
			})
		if err != nil {
			return "", err
		}
		if sink.result.Resumed {
			slog.ErrorContext(ctx, "qa.datagen.search_mixed_rebuild_running", slog.String("err", errSearchMixedRebuildRunning.Error()),
				slog.String("rebuild_id", sink.result.RebuildID))
			return "", errSearchMixedRebuildRunning
		}
		return sink.result.TargetIndex, nil
	}
}

// searchMixedRebuildSink decodes and stores the reindex JSON result without
// printing it.
type searchMixedRebuildSink struct {
	result searchReindexOutput
}

func (s *searchMixedRebuildSink) WriteJSON(ctx context.Context, payload json.RawMessage) error {
	if err := json.Unmarshal(payload, &s.result); err != nil {
		return searchReindexFailure(ctx, "the rebuild result sink could not decode the reindex result", err)
	}
	return nil
}

func (s *searchMixedRebuildSink) WriteText(ctx context.Context, _ string) error {
	return searchReindexFailure(ctx, "the rebuild result sink rejected text output", errSearchMixedRebuildText)
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
