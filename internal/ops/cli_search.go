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

var searchGroup = &clispec.Group{
	Use: "search", Short: "Control the native OpenSearch adapter", Long: "", Parent: opsGroup,
}

type searchVerifyInput struct {
	clispec.InputMarker
}

func searchVerifyOp(f *cli.Factory) clispec.Operation[searchVerifyInput] {
	return clispec.Operation[searchVerifyInput]{
		Name:     clispec.Name{Canonical: "verify", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDeployVerify), Reads: true},
		Group:    searchGroup,
		Short:    "Verify the configured native OpenSearch endpoint",
		New:      func() searchVerifyInput { return searchVerifyInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchVerifyInput, _ clispec.ResultSink) error {
			adapter, err := search.New(ctx, search.Config{
				Endpoint: f.Cfg.SearchEndpoint, CA: f.Cfg.SearchCA,
				Username: f.Cfg.SearchUsername, Password: f.Cfg.SearchPassword,
				RequestTimeout: f.Cfg.SearchRequestTimeout, MaxRetries: f.Cfg.SearchMaxRetries,
			})
			if err != nil {
				wrapped := fmt.Errorf("create search adapter: %w", err)
				telemetry.L(ctx).ErrorContext(ctx, "search.verify.adapter_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			if err := adapter.Ping(ctx); err != nil {
				wrapped := fmt.Errorf("verify search endpoint: %w", err)
				telemetry.L(ctx).ErrorContext(ctx, "search.verify.ping_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			_, err = adapter.IndexInfo(ctx, "node-pages-1")
			if err != nil {
				wrapped := fmt.Errorf("read search mapping: %w", err)
				telemetry.L(ctx).ErrorContext(ctx, "search.verify.mapping_failed", slog.String("err", wrapped.Error()))
				return wrapped
			}
			return nil
		},
	}
}
