package image_test

import (
	"context"
	"os/exec"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

// commandCase pairs a subtest name with a ci-image command.
//
// The name identifies the subtest, and build creates its command.
//
//	commandCase{name: "digest", build: digestCommand}
type commandCase struct {
	name  string
	build imagetest.Command
}

func rootHelpCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", helpFlag)
}

func digestHelpCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "digest", helpFlag)
}

func tagsHelpCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "tags", helpFlag)
}

func verifyNativeRunnerHelpCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "verify-native-runner", helpFlag)
}

func metadataHelpCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "metadata", helpFlag)
}

func missingOperationCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image")
}

func unknownOperationCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", unknownOperation)
}

func extraArgumentCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "digest", "extra")
}
