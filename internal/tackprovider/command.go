package tackprovider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/agoodkind/terraform-provider-pveguest/transport"
	"github.com/hashicorp/terraform-plugin-framework/diag"
)

const (
	// applyTimeoutSeconds is the limit of the container exec API.
	applyTimeoutSeconds = 3600

	// stderrTailBytes bounds the stderr text in a diagnostic.
	stderrTailBytes = 4096
)

// schemaTarget is the Tack installation that one tack_schema resource manages.
type schemaTarget struct {
	guest      transport.Guest
	installDir string
}

// statusArgv returns the read-only `migrate` command. The command has no
// --execute flag, and compose does not pull the image. With
// LOG_DISABLE_STDOUT, the command writes only the status document to stdout.
func statusArgv(target schemaTarget, operatorFlags []string) []string {
	argv := []string{
		"env", "-C", target.installDir,
		"docker", "compose", "run", "--rm", "--no-deps", "-T", "--pull", "never",
		"-e", "LOG_DISABLE_STDOUT=true",
		"tack-ops", "migrate", "--output", "json",
	}
	return append(argv, operatorFlags...)
}

// applyArgv returns the `migrate --execute` command that applies migrations
// through version and refuses to run unless the database is at expectFrom.
func applyArgv(target schemaTarget, operatorFlags []string, version int64, expectFrom int64) []string {
	return append(statusArgv(target, operatorFlags),
		"--execute",
		"--to", strconv.FormatInt(version, 10),
		"--expect-from", strconv.FormatInt(expectFrom, 10),
	)
}

// runCommand runs argv in the guest and returns its stdout. A transport
// failure or a nonzero exit status returns an error diagnostic.
func (data *providerData) runCommand(
	ctx context.Context,
	target schemaTarget,
	argv []string,
	timeoutSeconds int,
) ([]byte, diag.Diagnostics) {
	var diags diag.Diagnostics
	quoted := transport.QuoteCommand(argv)
	result, err := data.client.Run(ctx, target.guest, transport.Command{
		Argv:           argv,
		Stdin:          nil,
		TimeoutSeconds: timeoutSeconds,
	})
	if err != nil {
		slog.ErrorContext(ctx, "schema.command_failed", slog.String("guest", target.guest.String()),
			slog.String("err", err.Error()))
		var unreachable *transport.UnreachableError
		if errors.As(err, &unreachable) {
			diags.AddError("Tack guest is unreachable",
				fmt.Sprintf("The provider cannot run %s: %s", quoted, err.Error()))
			return nil, diags
		}
		diags.AddError("Proxmox API request failed",
			fmt.Sprintf("The provider cannot run %s: %s", quoted, err.Error()))
		return nil, diags
	}
	if result.ExitCode != 0 {
		err := fmt.Errorf("command exited with code %d", result.ExitCode)
		slog.ErrorContext(ctx, "schema.command_failed", slog.String("guest", target.guest.String()),
			slog.Int("exit_code", result.ExitCode), slog.String("err", err.Error()))
		diags.AddError("Tack command failed",
			fmt.Sprintf("The command %s in %s exited with code %d.\n%s",
				quoted, target.guest, result.ExitCode, stderrText(result.Stderr)))
		return nil, diags
	}
	return result.Stdout, diags
}

// readStatus runs the read-only `migrate` command and decodes its stdout.
func (data *providerData) readStatus(ctx context.Context, target schemaTarget) (schemaStatus, diag.Diagnostics) {
	var unread schemaStatus
	argv := statusArgv(target, data.operatorFlags)
	stdout, diags := data.runCommand(ctx, target, argv, 0)
	if diags.HasError() {
		return unread, diags
	}
	status, err := decodeStatus(ctx, stdout)
	if err != nil {
		diags.AddError("Tack migration status is unreadable",
			fmt.Sprintf("The output of %s in %s is not a status document: %s",
				transport.QuoteCommand(argv), target.guest, err.Error()))
		return unread, diags
	}
	slog.InfoContext(ctx, "schema.read", slog.String("guest", target.guest.String()),
		slog.Bool("migration_table_present", status.MigrationTablePresent),
		slog.Int64("applied_version", status.AppliedVersion),
		slog.String("image_commit", status.ImageCommit))
	return status, diags
}

// stderrText returns at most the last stderrTailBytes bytes of stderr.
func stderrText(stderr []byte) string {
	if len(stderr) == 0 {
		return "The command wrote nothing to stderr."
	}
	tail := stderr
	if len(tail) > stderrTailBytes {
		tail = tail[len(tail)-stderrTailBytes:]
	}
	text := strings.ToValidUTF8(string(tail), "")
	return fmt.Sprintf("The last %d bytes of stderr:\n%s", len(tail), text)
}

func diagnosticsText(diags diag.Diagnostics) string {
	parts := make([]string, 0, len(diags))
	for _, diagnostic := range diags {
		parts = append(parts, diagnostic.Summary()+": "+diagnostic.Detail())
	}
	return strings.Join(parts, "; ")
}
