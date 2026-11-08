// Package image defines commands for image build operations.
package image

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/ci/image/manifest"
	"goodkind.io/tack/internal/ci/image/metadata"
	"goodkind.io/tack/internal/ci/image/native"
	"goodkind.io/tack/internal/ci/image/reference"
)

const commandName = "ci-image"

var errUsage = errors.New(
	"usage: ci-image digest|tags|verify-native-runner|metadata|manifest",
)

// operationError prefixes an error with the operation name and exposes
// the original error through Unwrap.
type operationError struct {
	operation string
	err       error
}

func (e *operationError) Error() string {
	return e.operation + ": " + e.err.Error()
}

func (e *operationError) Unwrap() error {
	return e.err
}

// newOperationError wraps a non-nil error with the operation name and
// returns nil for a nil error.
func newOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return &operationError{operation: operation, err: err}
}

// NewCommand returns the ci-image command.
//
// The command registers subcommands that run image build operations
// implemented in packages under internal/ci/image.
//
//	if err := image.NewCommand().Execute(); err != nil {
//		slog.Error("ci-image.failed", slog.String("error", err.Error()))
//		os.Exit(1)
//	}
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           commandName,
		Short:         "Run an image build operation",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(*cobra.Command, []string) error {
			return errUsage
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		fileOperation("digest", "Write the image digest file", reference.WriteDigest),
		fileOperation("tags", "Write the image tag file", reference.WriteTags),
		outputOperation(
			"verify-native-runner",
			"Check the runner machine type against IMAGE_ARCH and print it",
			native.Verify,
		),
		outputOperation(
			"metadata",
			"Append build metadata to GITHUB_OUTPUT and print the selected tag or branch",
			metadata.Write,
		),
		contextOperation(
			"manifest",
			"Publish one multi-platform index for the native image digests under every tag",
			func(ctx context.Context, _ io.Writer) error {
				return manifest.Publish(ctx)
			},
		),
	)

	return root
}

func fileOperation(name, short string, run func() error) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return newOperationError(name, run())
		},
	}
}

// contextOperation creates a subcommand for an operation that uses the
// command context and writes progress to stderr.
func contextOperation(
	name, short string,
	run func(context.Context, io.Writer) error,
) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return newOperationError(name, run(command.Context(), command.ErrOrStderr()))
		},
	}
}

// outputOperation creates a subcommand for an operation that writes to
// the command output.
func outputOperation(name, short string, run func(io.Writer) error) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			stopSignals := handleBrokenPipe()
			err := run(command.OutOrStdout())

			stopSignals()

			return newOperationError(name, err)
		},
	}
}
