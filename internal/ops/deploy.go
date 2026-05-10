// Package ops: deploy.go owns the `./server ops deploy` family entry point
// and the deployContext value type that every deploy step receives. The
// default flow is build, push, pull, up, verify; subcommands let the
// operator stop after any one. The plan section 4 is the source of truth.

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"goodkind.io/tack/internal/config"
)

const (
	deployModeRegistry = "registry"
	deployModeOffline  = "offline"
)

// deployVerb is the named enum of deploy subcommands. The dispatcher switches
// on values of this type so the staticcheck-extra "bare string switch" gate
// stays green.
type deployVerb string

const (
	deployVerbHelp   deployVerb = "help"
	deployVerbBuild  deployVerb = "build"
	deployVerbPush   deployVerb = "push"
	deployVerbPull   deployVerb = "pull"
	deployVerbUp     deployVerb = "up"
	deployVerbVerify deployVerb = "verify"
)

// deployContext bundles every value a deploy step needs. Built once per
// invocation by newDeployContext and threaded through build/push/pull/up.
type deployContext struct {
	cfg           *config.Config
	mode          string
	registry      string
	dockerCtx     string
	composeFile   string
	commit        string
	tag           string
	imageRef      string
	latestRef     string
	timeout       time.Duration
	ghcrUser      string
	ghcrToken     string
	log           *slog.Logger
	pushedDigests []string
}

// runDeployCommand routes the deploy family. Default arm runs the full flow.
// Each subcommand maps to one focused step file.
func runDeployCommand(ctx context.Context, cfg *config.Config, args []string) error {
	dctx, err := newDeployContext(ctx, cfg)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return runDeployAll(ctx, dctx)
	}
	verb := deployVerb(args[0])
	switch verb {
	case deployVerbHelp:
		printDeployUsage()
		return nil
	case deployVerbBuild:
		return runDeployBuild(ctx, dctx)
	case deployVerbPush:
		return runDeployPush(ctx, dctx)
	case deployVerbPull:
		return runDeployPull(ctx, dctx)
	case deployVerbUp:
		return runDeployUp(ctx, dctx)
	case deployVerbVerify:
		return runDeployVerify(ctx, dctx)
	}
	printDeployUsage()
	err = fmt.Errorf("unknown deploy command %q", args[0])
	dctx.log.ErrorContext(ctx, "ops.deploy.unknown_verb",
		slog.String("verb", string(verb)), slog.String("err", err.Error()))
	return err
}

// runDeployAll runs the canonical end-to-end flow. Each step exits non-zero
// on failure, which short-circuits the chain and surfaces the first error.
func runDeployAll(ctx context.Context, dctx *deployContext) error {
	dctx.log.InfoContext(ctx, "ops.deploy.start",
		slog.String("commit", dctx.commit),
		slog.String("mode", dctx.mode),
		slog.String("docker_context", dctx.dockerCtx),
	)
	if err := runDeployBuild(ctx, dctx); err != nil {
		return err
	}
	if err := runDeployPush(ctx, dctx); err != nil {
		return err
	}
	if err := runDeployPull(ctx, dctx); err != nil {
		return err
	}
	if err := runDeployUp(ctx, dctx); err != nil {
		return err
	}
	if err := runDeployVerify(ctx, dctx); err != nil {
		return err
	}
	dctx.log.InfoContext(ctx, "ops.deploy.completed",
		slog.String("image_ref", dctx.imageRef),
	)
	return nil
}

// newDeployContext gathers config plus the current git commit and builds the
// canonical image refs. Failures here are early and loud: a bad deploy mode
// or an unconfigured docker context aborts before any container or registry
// side effects.
func newDeployContext(ctx context.Context, cfg *config.Config) (*deployContext, error) {
	slog.InfoContext(ctx, "ops.deploy.context.build")
	mode := strings.TrimSpace(cfg.DeployMode)
	if mode != deployModeRegistry && mode != deployModeOffline {
		err := fmt.Errorf("TACK_DEPLOY_MODE %q must be %q or %q",
			mode, deployModeRegistry, deployModeOffline)
		slog.ErrorContext(ctx, "ops.deploy.context.bad_mode",
			slog.String("mode", mode), slog.String("err", err.Error()))
		return nil, err
	}
	registry := strings.TrimSpace(cfg.DeployRegistry)
	dockerCtx := strings.TrimSpace(cfg.DeployDockerContext)
	composeFile := strings.TrimSpace(cfg.DeployRemoteComposeFile)
	timeoutSecs := cfg.DeployTimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = 600
	}
	commit, err := readGitCommit(ctx)
	if err != nil {
		return nil, err
	}
	imageRef := registry + ":" + commit
	latestRef := registry + ":latest"
	dctx := &deployContext{
		cfg:           cfg,
		mode:          mode,
		registry:      registry,
		dockerCtx:     dockerCtx,
		composeFile:   composeFile,
		commit:        commit,
		tag:           "latest",
		imageRef:      imageRef,
		latestRef:     latestRef,
		timeout:       time.Duration(timeoutSecs) * time.Second,
		ghcrUser:      strings.TrimSpace(os.Getenv("GHCR_USERNAME")),
		ghcrToken:     strings.TrimSpace(os.Getenv("GHCR_TOKEN")),
		log:           slog.Default(),
		pushedDigests: nil,
	}
	if mode == deployModeRegistry {
		if dctx.ghcrUser == "" || dctx.ghcrToken == "" {
			err := errors.New(
				"deploy registry mode requires GHCR_USERNAME and GHCR_TOKEN env vars")
			slog.ErrorContext(ctx, "ops.deploy.context.missing_creds",
				slog.String("mode", mode), slog.String("err", err.Error()))
			return nil, err
		}
	}
	if err := assertDockerContext(ctx, dockerCtx); err != nil {
		return nil, err
	}
	return dctx, nil
}

// readGitCommit returns the short HEAD SHA. Used as the canonical image tag
// component. The deploy step refuses to run if the working tree is not a git
// repository.
func readGitCommit(ctx context.Context) (string, error) {
	slog.DebugContext(ctx, "ops.deploy.git.rev_parse")
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short=12", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.git.rev_parse_failed",
			slog.String("err", err.Error()))
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		err := errors.New("git rev-parse HEAD returned empty string")
		slog.ErrorContext(ctx, "ops.deploy.git.rev_parse_empty",
			slog.String("err", err.Error()))
		return "", err
	}
	return commit, nil
}
