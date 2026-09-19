package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// deployGroup holds the read that follows a deploy. Images are built by the
// repository's build workflow and rolled by the configs deploy, so the only
// command here checks the outcome.
var deployGroup = &clispec.Group{
	Use: "deploy", Short: "Check the outcome of a deploy on this daemon",
	Long: "", Parent: opsGroup,
}

// deployVerifyInput carries the optional explicit image tag.
type deployVerifyInput struct {
	clispec.InputMarker
	Tag string
}

// deployVerifyOp declares `ops deploy verify`.
func deployVerifyOp(f *cli.Factory) clispec.Operation[deployVerifyInput] {
	return clispec.Operation[deployVerifyInput]{
		Name:     clispec.Name{Canonical: "verify", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsDeployVerify), Reads: true},
		Group:    deployGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Assert the app and audit-consumer containers run the deployed images",
		Long: "Reads the registry digest of each expected image from the daemon and " +
			"compares it with the digest the matching container runs. The expected " +
			"images are tack-server and tack-audit-consumer under TACK_DEPLOY_REGISTRY " +
			"at TACK_IMAGE_TAG, unless --tag names another tag.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[deployVerifyInput]{
			clispec.StringParam("tag", "image tag the containers must run", "", false,
				func(in *deployVerifyInput, v string) { in.Tag = v }),
		},
		New: func() deployVerifyInput {
			return deployVerifyInput{InputMarker: clispec.InputMarker{}, Tag: ""}
		},
		Run: func(ctx context.Context, in deployVerifyInput, sink clispec.ResultSink) error {
			return runDeployVerify(ctx, f.Cfg, sink, in.Tag)
		},
	}
}
