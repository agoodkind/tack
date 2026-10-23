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

// deployVerifyInput is the optional explicit image tag and the optional
// expected index digests of the two images.
type deployVerifyInput struct {
	clispec.InputMarker
	Tag            string
	ServerDigest   string
	ConsumerDigest string
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
		Long:     "Check every app and audit-consumer replica in TACK_DEPLOY_COMPOSE_PROJECT using Docker Compose project and service labels. Without digest flags, the command compares each replica's image digest with the digest the local Docker daemon records for the expected image tag. The expected images are tack-server and tack-audit-consumer under TACK_DEPLOY_REGISTRY at TACK_IMAGE_TAG; --tag overrides that tag. To compare image index digests, supply --tack-server-digest and --tack-audit-consumer-digest together. This mode requires the containerd image store and compares each replica's image index descriptor digest with the supplied digest for its service. Each digest must use sha256: followed by 64 lowercase hexadecimal digits.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[deployVerifyInput]{
			clispec.StringParam("tag", "image tag the containers must run", "", false,
				func(in *deployVerifyInput, v string) { in.Tag = v }),
			clispec.StringParam("tack-server-digest", "Require every app replica to match this image index digest (sha256: followed by 64 lowercase hexadecimal digits).", "", false,
				func(in *deployVerifyInput, v string) { in.ServerDigest = v }),
			clispec.StringParam("tack-audit-consumer-digest", "Require every audit-consumer replica to match this image index digest (sha256: followed by 64 lowercase hexadecimal digits).", "", false,
				func(in *deployVerifyInput, v string) { in.ConsumerDigest = v }),
		},
		New: func() deployVerifyInput {
			return deployVerifyInput{InputMarker: clispec.InputMarker{}, Tag: "", ServerDigest: "", ConsumerDigest: ""}
		},
		Run: func(ctx context.Context, in deployVerifyInput, sink clispec.ResultSink) error {
			digests, err := parseDeployIndexDigests(ctx, in.ServerDigest, in.ConsumerDigest)
			if err != nil {
				return err
			}
			return runDeployVerify(ctx, f.Cfg, sink, in.Tag, digests)
		},
	}
}
