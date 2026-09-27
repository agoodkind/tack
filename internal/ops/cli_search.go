package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
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
		Audit:    audit.Spec{Verb: string(audit.VerbOpsSearchVerify), Reads: true},
		Group:    searchGroup,
		Short:    "Verify the configured native OpenSearch endpoint",
		New:      func() searchVerifyInput { return searchVerifyInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ searchVerifyInput, _ clispec.ResultSink) error {
			if err := requireSearchProjections(ctx, f); err != nil {
				return err
			}
			return runSearchVerify(ctx, f)
		},
	}
}
