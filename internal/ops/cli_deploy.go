package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

const (
	// deployVerifyPayloadFormat is the media type of the deploy verify output.
	deployVerifyPayloadFormat = "text/plain; charset=utf-8"
	// deployReference identifies the Docker Compose deployment that deploy
	// verify reads. The read targets name each checked container.
	deployReference = "docker/compose"
	// deployContainerReference prefixes each container that deploy verify reads.
	deployContainerReference = "docker/containers/"
)

// deployGroup is the parent of the read that checks a deploy. The repository
// build workflow builds the images, and the configs deploy rolls them.
var deployGroup = &clispec.Group{
	Use: "deploy", Short: "Check the outcome of a deploy on this daemon",
	Long: "", Parent: opsGroup,
}

// deployVerifyInput is the optional explicit image tag, the optional service
// selection, and the optional expected index digests of the checked images.
type deployVerifyInput struct {
	Tag            string `json:"tag"`
	Service        string `json:"service"`
	ServerDigest   string `json:"tack_server_digest"`
	ConsumerDigest string `json:"tack_audit_consumer_digest"`
}

// deployVerifySpec declares `ops deploy verify` as a read of the Docker
// Compose deployment.
func deployVerifySpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbOpsDeployVerify), Reads: true, Class: audit.OperationRead,
		Targets: func(context.Context, json.RawMessage) ([]audit.Target, error) {
			return []audit.Target{externalTarget(deployReference)}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

// deployVerifyCommand builds `ops deploy verify`. With --execute, it records
// the read through the read lifecycle and prints one line per checked
// container. A refusal records a failed read.
func deployVerifyCommand(f *cli.Factory) *cobra.Command {
	in := deployVerifyInput{Tag: "", Service: "", ServerDigest: "", ConsumerDigest: ""}
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Assert the app and audit-consumer containers run the deployed images",
		Long:  "Check every app and audit-consumer replica in TACK_DEPLOY_COMPOSE_PROJECT using Docker Compose project and service labels. Without digest flags, the command compares each replica's image digest with the digest the local Docker daemon records for the expected image tag. The expected images are tack-server and tack-audit-consumer under TACK_DEPLOY_REGISTRY at TACK_IMAGE_TAG; --tag overrides that tag. To compare image index digests, supply --tack-server-digest and --tack-audit-consumer-digest together. This mode requires the containerd image store and compares each replica's image index descriptor digest with the supplied digest for its service. Each digest must use sha256: followed by 64 lowercase hexadecimal digits. On a guest that runs only the app service, --service app checks only the app replicas and their tack-server image. With --service app, the index digest mode takes --tack-server-digest alone and refuses --tack-audit-consumer-digest.",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&in.Tag, "tag", "", "image tag the containers must run")
	cmd.Flags().StringVar(&in.Service, "service", "", "Check only this Docker Compose service. The only accepted value is app. Omit the flag to check app and audit-consumer.")
	cmd.Flags().StringVar(&in.ServerDigest, "tack-server-digest", "", "Require every app replica to match this image index digest (sha256: followed by 64 lowercase hexadecimal digits).")
	cmd.Flags().StringVar(&in.ConsumerDigest, "tack-audit-consumer-digest", "", "Require every audit-consumer replica to match this image index digest (sha256: followed by 64 lowercase hexadecimal digits).")
	input := func() (json.RawMessage, error) {
		encoded, err := json.Marshal(in)
		if err != nil {
			slog.Error("ops.deploy.verify.input_encode_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("ops deploy verify: encode the command input: %w", err)
		}
		return encoded, nil
	}
	clispec.AttachRead(cmd, f, deployVerifySpec(), input, func(ctx context.Context) (audit.OperatorReadOutput, error) {
		services, err := parseDeployVerifyServices(ctx, in.Service)
		if err != nil {
			return audit.OperatorReadOutput{}, err
		}
		digests, err := parseDeployIndexDigests(ctx, in.ServerDigest, in.ConsumerDigest, services)
		if err != nil {
			return audit.OperatorReadOutput{}, err
		}
		return runDeployVerify(ctx, f.Cfg, in.Tag, services, digests)
	})
	return cmd
}

// replicaTargets returns one external target per checked container, in check
// order.
func replicaTargets(replicas []serviceReplica) []audit.Target {
	targets := make([]audit.Target, 0, len(replicas))
	for _, replica := range replicas {
		targets = append(targets, externalTarget(deployContainerReference+strings.TrimPrefix(replica.Container, "/")))
	}
	return targets
}
