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

type searchModelReplaceInput struct{ clispec.InputMarker }

type searchModelReplaceResult struct {
	clispec.ResultMarker
	Command         string `json:"command"`
	PreviousModelID string `json:"previous_model_id"`
	ModelID         string `json:"model_id"`
}

func searchModelReplaceOp(f *cli.Factory) clispec.Operation[searchModelReplaceInput] {
	return clispec.Operation[searchModelReplaceInput]{
		Name: clispec.Name{Canonical: "replace-model", CLIOverride: ""}, Lifetime: clispec.Permanent,
		Audit: audit.Spec{Verb: string(audit.VerbOpsSearchModelReplace), Mutates: true}, Group: searchGroup,
		Short: "Replace the registered search model",
		Long: "The command undeploys and deletes the registered pinned model, then registers, deploys, and verifies a new copy. " +
			"Run ops search reindex --execute afterward to rebuild indexes that reference the previous model ID. " +
			"Without --execute, this command reports a dry run.",
		New: func() searchModelReplaceInput { return searchModelReplaceInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchModelReplaceInput, sink clispec.ResultSink) (runErr error) {
			logger := telemetry.L(ctx)
			certificate, err := config.LoadSearchCA(ctx, f.Cfg.SearchCA)
			if err != nil {
				wrapped := fmt.Errorf("load search CA: %w", err)
				logger.ErrorContext(ctx, "search.model_replace.ca_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			pass := f.Cfg.SearchPassword
			adapter, err := search.New(ctx, search.Config{Endpoint: f.Cfg.SearchEndpoint, CA: certificate, Username: f.Cfg.SearchUsername, Password: pass, RequestTimeout: f.Cfg.SearchRequestTimeout, MaxRetries: f.Cfg.SearchMaxRetries})
			if err != nil {
				wrapped := fmt.Errorf("create search adapter: %w", err)
				logger.ErrorContext(ctx, "search.model_replace.adapter_failed", slog.String("err", wrapped.Error()))
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
			previous, model, err := adapter.ReplacePinnedModel(ctx)
			if err != nil {
				wrapped := fmt.Errorf("replace search model: %w", err)
				logger.ErrorContext(ctx, "search.model_replace.failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			if err := clispec.WriteJSONValue(ctx, sink, searchModelReplaceResult{
				ResultMarker: clispec.ResultMarker{}, Command: "ops.search.replace-model",
				PreviousModelID: previous, ModelID: model.ID,
			}); err != nil {
				wrapped := fmt.Errorf("write search model replacement result: %w", err)
				logger.ErrorContext(ctx, "search.model_replace.result_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			return nil
		},
	}
}
